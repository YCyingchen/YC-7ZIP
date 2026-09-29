package server

// QQ 官方机器人的"扫码绑定"。
//
// 官方开放平台提供的是一条扫码绑定通道（不是登录，也不是第三方协议）：
// 本地生成一个随机 key 去换 task_id，把 task_id 拼成链接做成二维码让人用手机 QQ 扫；
// 扫完平台回一个用那把 key 加密过的 AppSecret，本地 AES-256-GCM 解开就拿到了凭据。
// 整条链路里 key 从不离开这台服务器，所以浏览器只拿到一张图和一个会话号——
// 就算有人截获了轮询响应，也解不开里面那个 secret。

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/qrcode"
)

const (
	// QQ 官方开放平台的两个接口地址（q.qq.com 是生产环境的 host）。
	qqBindCreatePath = "/lite/create_bind_task"
	qqBindPollPath   = "/lite/poll_bind_result"
	// 二维码里装的页面。
	qqBindConnectPath = "/qqbot/openclaw/connect.html"
	// source 会拼进二维码链接，便于官方侧区分是哪个应用发起的绑定。
	qqBindSource = "yc7zip"

	// qqBindTTL 是会话寿命。二维码本身会过期，会话留太久只会让人以为还能扫。
	qqBindTTL = 5 * time.Minute
	// qqBindMinInterval 是两次向官方查询之间的最短间隔：界面每两秒问一次我们，
	// 不必每次都原样转发出去。
	qqBindMinInterval = 1500 * time.Millisecond
	// qqBindMaxSessions 限制同时挂着的会话数——"生成二维码"是可以被反复点的，
	// 没有上限就是一个能攒内存的按钮。
	qqBindMaxSessions = 8
	// qqBindTimeout 是单次出站调用的上限。
	qqBindTimeout = 10 * time.Second

	// 会话状态。前三个与官方 poll 的 status 对齐（0 NONE / 1 PENDING / 2 COMPLETED / 3 EXPIRED），
	// failed 是我们自己产生的（协议错、解密失败）。
	qqBindPending   = "pending"
	qqBindCompleted = "completed"
	qqBindExpired   = "expired"
	qqBindFailed    = "failed"
)

// qqBindSession 是一次绑定过程。
type qqBindSession struct {
	ID        string
	TaskID    string
	CreatedAt time.Time
	ExpiresAt time.Time

	// Key 是本地生成的随机数（base64），用来解 AppSecret；不出服务器，也不写日志。
	Key string

	// 下面几项随轮询更新。
	Status     string
	AppID      string
	AppSecret  string
	UserOpenID string
	// Err 是"这次没问到"的原因。网络抖动不该让会话直接作废，
	// 所以它只是一个提示，状态仍是 pending；协议层出错才转 failed。
	Err string

	lastPoll time.Time
}

// qqBindStore 管着一批绑定会话。
type qqBindStore struct {
	mu       sync.Mutex
	sessions map[string]*qqBindSession

	// host 与 client 留成字段，测试可以指到 httptest 上。
	host   string
	client *http.Client
	// minInterval 是两次向官方查询之间的最短间隔。
	minInterval time.Duration
}

func newQQBindStore() *qqBindStore {
	return &qqBindStore{
		sessions:    map[string]*qqBindSession{},
		host:        "https://q.qq.com",
		client:      &http.Client{Timeout: qqBindTimeout},
		minInterval: qqBindMinInterval,
	}
}

// start 开一次绑定：生成本地 key → 换 task_id → 拼出二维码要装的那条链接。
func (b *qqBindStore) start(ctx context.Context) (*qqBindSession, string, error) {
	keyRaw := make([]byte, 32)
	if _, err := rand.Read(keyRaw); err != nil {
		return nil, "", errors.New("无法生成随机密钥：" + err.Error())
	}
	key := base64.StdEncoding.EncodeToString(keyRaw)

	status, body, err := b.post(ctx, qqBindCreatePath, map[string]any{"key": key})
	if err != nil {
		return nil, "", errors.New("创建绑定任务失败：" + err.Error())
	}
	if !httpOK(status) {
		return nil, "", errors.New("创建绑定任务失败：" + detailOf(status, body))
	}
	var out struct {
		RetCode int    `json:"retcode"`
		Msg     string `json:"msg"`
		Data    struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, "", fmt.Errorf("创建绑定任务失败：响应不是 JSON（%s）", truncRunes(strings.Join(strings.Fields(string(body)), " "), notifySnippet))
	}
	if out.RetCode != 0 {
		return nil, "", errors.New("创建绑定任务失败：" + firstNonEmpty(out.Msg, "retcode="+strconv.Itoa(out.RetCode)))
	}
	if out.Data.TaskID == "" {
		return nil, "", errors.New("创建绑定任务失败：响应里没有 task_id")
	}

	// 会话号也要不可猜：拿到它就能读到解好的 AppSecret。
	id, err := randomHex(16)
	if err != nil {
		return nil, "", errors.New("无法生成会话号：" + err.Error())
	}

	now := time.Now()
	sess := &qqBindSession{
		ID:        id,
		TaskID:    out.Data.TaskID,
		Key:       key,
		Status:    qqBindPending,
		CreatedAt: now,
		ExpiresAt: now.Add(qqBindTTL),
	}
	b.mu.Lock()
	b.sweepLocked(now)
	b.sessions[id] = sess
	b.mu.Unlock()

	return sess, b.connectURL(out.Data.TaskID), nil
}

// connectURL 拼二维码里那条链接。
//
// _wv=2 是官方页面认的版本参数（原实现里就带着），少了它页面不一定认得。
func (b *qqBindStore) connectURL(taskID string) string {
	return b.host + qqBindConnectPath +
		"?task_id=" + url.QueryEscape(taskID) +
		"&source=" + url.QueryEscape(qqBindSource) +
		"&_wv=2"
}

// poll 问一次官方，返回会话的最新状态。
//
// 浏览器只轮询我们，由我们转发：key 留在服务器上，前端拿不到，也就没法自己解开 secret。
func (b *qqBindStore) poll(ctx context.Context, id string) (qqBindSession, error) {
	b.mu.Lock()
	sess, ok := b.sessions[id]
	if !ok {
		b.mu.Unlock()
		return qqBindSession{}, errors.New("绑定会话不存在或已过期，请重新生成二维码")
	}
	now := time.Now()
	if sess.Status == qqBindPending && now.After(sess.ExpiresAt) {
		sess.Status = qqBindExpired
		sess.Err = "二维码已过期"
	}
	// 状态已定、或距上次问官方还不到间隔，就直接把现状回给界面。
	if sess.Status != qqBindPending || now.Sub(sess.lastPoll) < b.minInterval {
		out := *sess
		b.mu.Unlock()
		return out, nil
	}
	sess.lastPoll = now
	taskID, key := sess.TaskID, sess.Key
	sweepAt := now
	b.mu.Unlock()

	// 下一轮的清理借这次机会做，避免再起一个定时器。
	b.mu.Lock()
	b.sweepLocked(sweepAt)
	b.mu.Unlock()

	status, body, err := b.post(ctx, qqBindPollPath, map[string]any{"task_id": taskID})
	if err != nil {
		return b.setErr(id, "查询绑定结果失败："+err.Error())
	}
	if !httpOK(status) {
		return b.setErr(id, "查询绑定结果失败："+detailOf(status, body))
	}
	var out struct {
		RetCode int    `json:"retcode"`
		Msg     string `json:"msg"`
		Data    struct {
			Status     int    `json:"status"`
			AppID      string `json:"bot_appid"`
			Encrypt    string `json:"bot_encrypt_secret"`
			UserOpenID string `json:"user_openid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return b.setErr(id, "查询绑定结果失败：响应不是 JSON")
	}
	if out.RetCode != 0 {
		return b.fail(id, "查询绑定结果失败："+firstNonEmpty(out.Msg, "retcode="+strconv.Itoa(out.RetCode)))
	}

	switch out.Data.Status {
	case 2: // COMPLETED
		secret, err := decryptBindSecret(key, out.Data.Encrypt)
		if err != nil {
			return b.fail(id, "解密 AppSecret 失败："+err.Error())
		}
		return b.update(id, func(s *qqBindSession) {
			s.Status = qqBindCompleted
			s.AppID = out.Data.AppID
			s.AppSecret = secret
			s.UserOpenID = out.Data.UserOpenID
			s.Err = ""
		})
	case 3: // EXPIRED
		return b.update(id, func(s *qqBindSession) {
			s.Status = qqBindExpired
			s.Err = "二维码已过期，请重新生成"
		})
	default: // 0 NONE / 1 PENDING：还没扫
		return b.update(id, func(s *qqBindSession) { s.Err = "" })
	}
}

// setErr 记下"这次没问到"的原因，但让会话继续 pending：网络抖一下不该让用户
// 重新扫码，界面过两秒会再问一次。
func (b *qqBindStore) setErr(id, msg string) (qqBindSession, error) {
	return b.update(id, func(s *qqBindSession) { s.Err = msg })
}

// fail 把会话定成失败：这是协议层的问题，再问下去也是一样的结果。
func (b *qqBindStore) fail(id, msg string) (qqBindSession, error) {
	return b.update(id, func(s *qqBindSession) {
		s.Status = qqBindFailed
		s.Err = msg
	})
}

func (b *qqBindStore) update(id string, fn func(*qqBindSession)) (qqBindSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sess, ok := b.sessions[id]
	if !ok {
		return qqBindSession{}, errors.New("绑定会话不存在或已过期，请重新生成二维码")
	}
	fn(sess)
	return *sess, nil
}

// sweepLocked 清掉过期会话，并保证不超过会话数上限。调用方持锁。
func (b *qqBindStore) sweepLocked(now time.Time) {
	for id, sess := range b.sessions {
		// 已完成/失败的会话留一小会儿：界面可能正要来取那份凭据。
		if now.After(sess.ExpiresAt) || (sess.Status != qqBindPending && now.Sub(sess.lastPoll) > time.Minute) {
			delete(b.sessions, id)
		}
	}
	for len(b.sessions) >= qqBindMaxSessions {
		var oldestID string
		var oldest time.Time
		for id, sess := range b.sessions {
			if oldestID == "" || sess.CreatedAt.Before(oldest) {
				oldestID, oldest = id, sess.CreatedAt
			}
		}
		delete(b.sessions, oldestID)
	}
}

// post 向官方发一个 JSON 请求。响应只读够判断的量。
func (b *qqBindStore) post(ctx context.Context, path string, payload any) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.host+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := b.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	return res.StatusCode, body, nil
}

// decryptBindSecret 解开官方回传的 AppSecret。
//
// 密文是 base64：前 12 字节 IV，中间密文，最后 16 字节 GCM tag；key 是本地那个
// 随机数的 base64 原文。这一步只能在本机做——key 从没离开过这台服务器。
func decryptBindSecret(keyB64, encB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", errors.New("本地密钥不是合法的 base64")
	}
	if len(key) != 32 {
		return "", fmt.Errorf("本地密钥应为 32 字节，实际 %d", len(key))
	}
	if strings.TrimSpace(encB64) == "" {
		return "", errors.New("官方没有回传密文")
	}
	raw, err := base64.StdEncoding.DecodeString(encB64)
	if err != nil {
		return "", errors.New("密文不是合法的 base64")
	}
	if len(raw) < 12+16 {
		return "", errors.New("密文长度不对")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := raw[:12]
	tag := raw[len(raw)-16:]
	sealed := make([]byte, 0, len(raw))
	sealed = append(sealed, raw[12:len(raw)-16]...)
	sealed = append(sealed, tag...)

	plain, err := gcm.Open(nil, iv, sealed, nil)
	if err != nil {
		return "", errors.New("解不开：密文与本地密钥不匹配")
	}
	return string(plain), nil
}

// randomHex 返回 n 字节的随机十六进制串。
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ---------------------------------------------------------------- 处理器

// handleQQBindStart 生成一张新的绑定二维码。
//
// 二维码在服务端画好（SVG 字符串）再给界面：项目零依赖，不想为一张图引入
// QR 库，也不打算把二维码内容丢给某个在线服务去生成。
func (s *Server) handleQQBindStart(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), qqBindTimeout)
	defer cancel()

	sess, link, err := s.qqBind.start(ctx)
	if err != nil {
		// 502：错在对面（或出站网络），不是这个请求写错了。
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	svg, err := qrcode.SVG(link, 8)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "生成二维码失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sess.ID,
		"qr_svg":     svg,
		"qr_url":     link,
		"expires_in": int(time.Until(sess.ExpiresAt).Seconds()),
	})
}

// handleQQBindPoll 由界面每两秒问一次。
func (s *Server) handleQQBindPoll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), qqBindTimeout)
	defer cancel()

	sess, err := s.qqBind.poll(ctx, r.URL.Query().Get("session"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	out := map[string]any{
		"status":     sess.Status,
		"error":      sess.Err,
		"expires_in": int(time.Until(sess.ExpiresAt).Seconds()),
	}
	if sess.Status == qqBindCompleted {
		out["app_id"] = sess.AppID
		out["app_secret"] = sess.AppSecret
		out["user_openid"] = sess.UserOpenID
	}
	writeJSON(w, http.StatusOK, out)
}
