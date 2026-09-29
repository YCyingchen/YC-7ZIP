package server

// 通知渠道：任务结束时把结果推到用户配好的机器人或 Webhook。
//
// 三件事贯穿这个文件：
//  1. 发送永远异步、失败只写日志。通知是"顺带告诉一声"，绝不能因为它把一次
//     已经落盘成功的任务变成失败任务。
//  2. 这些平台"HTTP 200 + 响应体里的错误码"是常态，所以每个发送器都要读响应体；
//     只看状态码会把"webhook key 填错了"报成"发送成功"。
//  3. 配置里躺着 AppSecret 这种拿到就等于把机器人交出去的值，所以落盘 0600、
//     回给界面时只报"有没有"。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/job"
)

const (
	// notifyFileName 是数据目录下的配置文件名，与 ui/、history/ 平级。
	notifyFileName = "notifications.json"

	// 渠道类型。QQ 走的是官方 v2 API，不是第三方协议。
	notifyWeCom    = "wecom"
	notifyDingTalk = "dingtalk"
	notifyFeishu   = "feishu"
	notifyQQBot    = "qqbot"
	notifyWebhook  = "webhook"

	// notifyMaxChannels 限制渠道条数：每条都要在任务结束时发一次，
	// 手滑粘十几条既没意义，又会让每次任务多出一串请求。
	notifyMaxChannels = 20
	// notifyTimeout 是单个渠道的发送上限。任务这时已经结束了，这里只是把消息
	// 送出去，不该让一个连不上的地址一直挂着。
	notifyTimeout = 10 * time.Second
	// notifySnippet 限制回给界面的响应片段长度：全量回显会把一个报错页整屏糊在设置面板里。
	notifySnippet = 300
	// notifyNameMax 限制渠道名长度（按字符数）。
	notifyNameMax = 40

	// QQ 官方机器人的两个固定地址。
	qqTokenURL = "https://bots.qq.com/app/getAppAccessToken"
	qqAPIBase  = "https://api.sgroup.qq.com"
	// qqTokenTTL 是响应里没给 expires_in 时的兜底（官方文档约 7200 秒）。
	qqTokenTTL = 7200 * time.Second
	// qqTokenMargin 提前续期：卡在最后一秒换 token 正好会撞上网络抖动。
	qqTokenMargin = 5 * time.Minute
)

// NotifyChannel 是一个渠道的配置。
//
// 五种类型的字段挤在同一个结构里，而不是每类一个：界面上它们本来就排在
// 同一张列表里增删改，拆开反而要多一层翻译；用不到的字段 omitempty 之后
// 也不出现在 JSON 里。
type NotifyChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type 决定用哪个发送器：wecom / dingtalk / feishu / qqbot / webhook。
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`

	// URL 是前四种"给个地址就能发"的渠道的地址。
	URL string `json:"url,omitempty"`
	// HeaderName/HeaderValue 是通用 webhook 的可选附加头（例如 Authorization）。
	HeaderName  string `json:"header_name,omitempty"`
	HeaderValue string `json:"header_value,omitempty"`

	// QQ 官方机器人：注册开发者 → 创建应用拿到的两个值。
	AppID     string `json:"app_id,omitempty"`
	AppSecret string `json:"app_secret,omitempty"`
	// TargetType 是 group / user，TargetID 是群或单聊的 openid。
	TargetType string `json:"target_type,omitempty"`
	TargetID   string `json:"target_id,omitempty"`

	// HasSecret / HasHeaderValue 只在回读时由服务端填：告诉界面"这一项存过"，
	// 但不再把原文送回去（见 snapshot）。
	HasSecret      bool `json:"has_secret,omitempty"`
	HasHeaderValue bool `json:"has_header_value,omitempty"`
}

// notifyTriggers 是两个全局触发开关。
type notifyTriggers struct {
	Done  bool `json:"done"`
	Error bool `json:"error"`
}

// notifyFile 是磁盘上的形状。
type notifyFile struct {
	Enabled  notifyTriggers  `json:"enabled"`
	Channels []NotifyChannel `json:"channels"`
}

// notifyPayload 是接口上的形状：触发开关平铺，界面少一层解包。
type notifyPayload struct {
	Done     bool            `json:"done"`
	Error    bool            `json:"error"`
	Channels []NotifyChannel `json:"channels"`
}

// notifyStore 负责读写渠道配置，并持有发消息要用的 HTTP 客户端与 token 缓存。
type notifyStore struct {
	path string

	mu   sync.Mutex
	conf notifyFile

	// client 是所有出站请求共用的。不用 http.DefaultClient：它是无超时的，
	// 一个不回话的 webhook 会让 goroutine 一直挂着。
	client *http.Client
	// tokenURL / apiBase 是 QQ 那两个地址，留成字段是为了让测试能指到 httptest 上，
	// 不必为可测性在别处开一个只给测试用的开关。
	tokenURL string
	apiBase  string

	// qqTokens 缓存 QQ 的 access_token：官方有效期约 7200 秒且换取接口有频控，
	// 每条消息都去换一次既慢又会被拦。
	tokenMu  sync.Mutex
	qqTokens map[string]*qqToken
}

type qqToken struct {
	// secret 一起存着：换了应用还拿着旧 token 会 401，所以密钥变了就重取。
	secret    string
	token     string
	expiresAt time.Time
}

func newNotifyStore(dataDir string) *notifyStore {
	n := &notifyStore{
		client:   &http.Client{Timeout: notifyTimeout},
		tokenURL: qqTokenURL,
		apiBase:  qqAPIBase,
		qqTokens: map[string]*qqToken{},
	}
	if dataDir != "" {
		n.path = filepath.Join(dataDir, notifyFileName)
	}
	n.load()
	return n
}

// load 读磁盘上的配置；坏了或读不出来就用默认值。
//
// 比 history 那处更要紧：坏掉的渠道配置既不能让设置面板打不开，更不能让服务起不来
// ——它本来只是"顺带的一条消息"。
func (n *notifyStore) load() {
	if n.path == "" {
		return
	}
	raw, err := os.ReadFile(n.path)
	if err != nil {
		return
	}
	var f notifyFile
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	n.conf = sanitizeNotify(f)
}

// sanitizeNotify 收敛一份配置：丢掉认不出的类型、补齐缺省、限长限条数。
//
// 刻意不在这里校验地址是否可用：那只该在"发送"和"测试"时报错。
// 保存这一关卡住的话，"先填一半、回头再补"就没法分段做了。
func sanitizeNotify(f notifyFile) notifyFile {
	out := notifyFile{Enabled: f.Enabled}
	for _, ch := range f.Channels {
		ch.Type = strings.TrimSpace(ch.Type)
		if !isNotifyType(ch.Type) {
			continue
		}
		ch.ID = strings.TrimSpace(ch.ID)
		if ch.ID == "" {
			ch.ID = newChannelID()
		}
		ch.Name = strings.TrimSpace(ch.Name)
		if ch.Name == "" {
			ch.Name = notifyTypeName(ch.Type)
		}
		ch.Name = truncRunes(ch.Name, notifyNameMax)
		ch.URL = strings.TrimSpace(ch.URL)
		ch.HeaderName = strings.TrimSpace(ch.HeaderName)
		ch.AppID = strings.TrimSpace(ch.AppID)
		ch.TargetID = strings.TrimSpace(ch.TargetID)
		if ch.TargetType != "user" {
			ch.TargetType = "group"
		}
		// 这两个只是回读时告诉界面"存过"，不该被客户端带进来。
		ch.HasSecret = false
		ch.HasHeaderValue = false
		out.Channels = append(out.Channels, ch)
	}
	if len(out.Channels) > notifyMaxChannels {
		out.Channels = out.Channels[:notifyMaxChannels]
	}
	return out
}

// save 整体替换配置并落盘。
//
// 先写临时文件再改名，中途断电不会留下半个 JSON —— 这个文件里还放着 AppSecret。
func (n *notifyStore) save(f notifyFile) error {
	f = sanitizeNotify(f)

	n.mu.Lock()
	// 界面拿到的是脱敏后的配置，整体回传时密钥那两栏是空的，这里按 id 补回去。
	//
	// 必须放在 save 里，而不是只放在"保存渠道"那个处理器里：界面上点一下渠道开关、
	// 换一下触发时机，走的都是同一次整体替换——漏掉这一步就会把 AppSecret 抹掉。
	for i := range f.Channels {
		if f.Channels[i].AppSecret != "" && f.Channels[i].HeaderValue != "" {
			continue
		}
		for _, old := range n.conf.Channels {
			if old.ID == "" || old.ID != f.Channels[i].ID {
				continue
			}
			if f.Channels[i].AppSecret == "" {
				f.Channels[i].AppSecret = old.AppSecret
			}
			if f.Channels[i].HeaderValue == "" {
				f.Channels[i].HeaderValue = old.HeaderValue
			}
			break
		}
	}
	n.conf = f
	path := n.path
	n.mu.Unlock()

	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	// 0600 而不是 ui/settings.json 那样的 0644：这里存着机器人的 AppSecret，
	// 同机其他账号不该读得到。
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// snapshot 返回给界面看的配置：密钥只报"有没有"，不报"是什么"。
//
// 取舍：AppSecret 与自定义头（常放 Authorization）一旦泄露就等于把机器人交出去，
// 而这个响应会留在浏览器、访问日志、截图里。代价是界面必须把这两个字段原样回传，
// 服务端按 id 认出"这条没改密钥"再把旧的补回去（见 withSecrets）——多这一层，
// 换来的是密钥不再每次打开设置面板就出一次门。
func (n *notifyStore) snapshot() notifyPayload {
	n.mu.Lock()
	defer n.mu.Unlock()

	out := notifyPayload{
		Done:     n.conf.Enabled.Done,
		Error:    n.conf.Enabled.Error,
		Channels: make([]NotifyChannel, 0, len(n.conf.Channels)),
	}
	for _, ch := range n.conf.Channels {
		ch.HasSecret = ch.AppSecret != ""
		ch.HasHeaderValue = ch.HeaderValue != ""
		ch.AppSecret = ""
		ch.HeaderValue = ""
		out.Channels = append(out.Channels, ch)
	}
	return out
}

// active 返回触发开关与当前启用的渠道（含密钥，发送要用）。
func (n *notifyStore) active() (notifyTriggers, []NotifyChannel) {
	n.mu.Lock()
	defer n.mu.Unlock()

	triggers := n.conf.Enabled
	var out []NotifyChannel
	for _, ch := range n.conf.Channels {
		if ch.Enabled {
			out = append(out, ch)
		}
	}
	return triggers, out
}

// withSecrets 把界面因故没有回传的密钥补回去。
//
// 界面拿到的是脱敏后的配置，保存时那两个框是空的；空值在这里解读为
// "这条密钥不动"，而不是"把密钥清掉"——想清掉就删掉整条渠道重建。
// 没带 id（或 id 对不上）的渠道按新渠道处理，不会去蹭别人的密钥。
func (n *notifyStore) withSecrets(ch NotifyChannel) NotifyChannel {
	if ch.ID == "" || (ch.AppSecret != "" && ch.HeaderValue != "") {
		return ch
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, old := range n.conf.Channels {
		if old.ID != ch.ID {
			continue
		}
		if ch.AppSecret == "" {
			ch.AppSecret = old.AppSecret
		}
		if ch.HeaderValue == "" {
			ch.HeaderValue = old.HeaderValue
		}
		break
	}
	return ch
}

func isNotifyType(t string) bool {
	switch t {
	case notifyWeCom, notifyDingTalk, notifyFeishu, notifyQQBot, notifyWebhook:
		return true
	}
	return false
}

// notifyTypeName 是渠道没起名时的默认名，与界面上的中文类型名一致。
func notifyTypeName(t string) string {
	switch t {
	case notifyWeCom:
		return "企业微信机器人"
	case notifyDingTalk:
		return "钉钉机器人"
	case notifyFeishu:
		return "飞书机器人"
	case notifyQQBot:
		return "QQ 机器人"
	case notifyWebhook:
		return "Webhook"
	}
	return t
}

// newChannelID 给渠道一个稳定 id。
//
// PUT 是整体替换，服务端得靠它认出"这是原来那条"，才能把界面没回传的密钥补回去。
func newChannelID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// 取不到随机数也得能用：时间戳足以把同一份配置里的几条渠道分开。
		return "ch" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

// truncRunes 按字符截断，避免把一个 UTF-8 字符劈成两半。
func truncRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// ---------------------------------------------------------------- 消息内容

// notifyMessage 是一条要发出去的消息。
//
// 各平台的报文格式不同，但"标题 + 若干行正文"这一层是共用的，所以先把内容定下来，
// 再由各发送器翻译成自己那份 JSON。
type notifyMessage struct {
	Title   string
	Lines   []string
	Event   string
	Service string
	Version string
	// Entry 是原始那条记录：通用 webhook 用它拼结构化字段。
	Entry HistoryEntry
}

func (m notifyMessage) Body() string { return strings.Join(m.Lines, "\n") }

// Markdown 给企业微信与钉钉：两者都认 markdown 子集，标题用 ###。
func (m notifyMessage) Markdown() string { return "### " + m.Title + "\n" + m.Body() }

// PlainText 给飞书、QQ 与 webhook：纯文本消息里 markdown 符号只会原样露出来。
func (m notifyMessage) PlainText() string { return m.Title + "\n" + m.Body() }

// buildNotifyMessage 把一条历史翻成通知内容。
//
// 复用的是历史那条记录：两者要说的本来就是同一件事（从哪来、写到哪去、多大、
// 用了多久），另攒一份字段只会让两边慢慢长歪。
func (s *Server) buildNotifyMessage(e HistoryEntry) notifyMessage {
	kind := "压缩"
	if e.Kind == string(job.KindExtract) {
		kind = "解压"
	}
	status := "完成"
	if e.Status == string(job.StatusError) {
		status = "失败"
	}

	m := notifyMessage{
		Title:   "YC-7ZIP " + kind + status,
		Event:   e.Kind + "." + e.Status,
		Service: s.serviceURL(),
		Version: s.cfg.Version,
		Entry:   e,
	}
	if len(e.Sources) > 0 {
		m.Lines = append(m.Lines, "来源："+summarizeSources(e.Sources))
	}
	m.Lines = append(m.Lines, "输出："+outputSummary(e))
	m.Lines = append(m.Lines, "用时："+fmtDurationMS(e.DurationMS))
	if e.TotalBytes > 0 {
		m.Lines = append(m.Lines, "总大小："+humanBytes(e.TotalBytes))
	}
	if e.Status == string(job.StatusError) && e.Message != "" {
		m.Lines = append(m.Lines, "错误："+e.Message)
	}
	m.Lines = append(m.Lines, "服务："+m.Service)
	return m
}

// testMessage 是"发送测试"用的内容：写得像真通知，标题却点明是测试，
// 免得群里有人把它当成一次真的压缩。
func (s *Server) testMessage() notifyMessage {
	e := HistoryEntry{
		ID: "test", Kind: string(job.KindCompress), Status: string(job.StatusDone),
		Sources:    []string{"/vol1/1000/示例.txt"},
		OutputMode: string(job.OutputServer), OutputDir: "/vol1/1000",
		Outputs:    []HistoryFile{{Name: "示例.7z", Size: 1048576}},
		TotalBytes: 1048576, DurationMS: 12340,
		FinishedAt: time.Now(),
	}
	m := s.buildNotifyMessage(e)
	m.Title = "YC-7ZIP 通知测试"
	m.Event = "test"
	return m
}

// summarizeSources 列出来源，多了就只报条数：一条消息里挂二十个路径没人看。
func summarizeSources(sources []string) string {
	const max = 3
	if len(sources) <= max {
		return strings.Join(sources, "、")
	}
	return strings.Join(sources[:max], "、") + " 等 " + strconv.Itoa(len(sources)) + " 项"
}

// outputSummary 说清结果落在哪儿，以及产物叫什么。
func outputSummary(e HistoryEntry) string {
	where := e.OutputDir
	if e.OutputMode != string(job.OutputServer) || where == "" {
		// 下载到本机的产物落在任务工作区里，那个路径对人没有意义，只报文件名。
		where = "下载到本机"
		if len(e.Outputs) > 0 {
			return where + "（" + outputNamesLabel(e.Outputs) + "）"
		}
		return where
	}
	if len(e.Outputs) > 0 {
		return filepath.Join(where, outputNamesLabel(e.Outputs))
	}
	return where
}

func outputNamesLabel(files []HistoryFile) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		return files[0].Name
	}
	return files[0].Name + " 等 " + strconv.Itoa(len(files)) + " 项"
}

// fmtDurationMS 把毫秒写成人话。
func fmtDurationMS(ms int64) string {
	switch {
	case ms < 1000:
		return strconv.FormatInt(ms, 10) + " 毫秒"
	case ms < 60000:
		return fmt.Sprintf("%.1f 秒", float64(ms)/1000)
	case ms < 3600000:
		return fmt.Sprintf("%d 分 %d 秒", ms/60000, (ms%60000)/1000)
	default:
		return fmt.Sprintf("%d 时 %d 分", ms/3600000, (ms%3600000)/60000)
	}
}

// serviceURL 给出写进通知正文的服务地址。
//
// 刻意不新增配置项：一句"这条是从哪儿发出来的"，用现成的监听地址就够。
// -addr 允许为空（只监听 unix socket，飞牛应用包里正是这种），那时退回
// BasePath，再退回 "YC-7ZIP"——宁可能少一点信息，也不为这句话多一个必填项。
func (s *Server) serviceURL() string {
	base := strings.TrimSuffix(strings.TrimSpace(s.cfg.BasePath), "/")
	addr := strings.TrimSpace(s.cfg.Addr)
	if addr != "" {
		host := addr
		if strings.HasPrefix(host, ":") {
			host = "127.0.0.1" + host
		}
		return "http://" + host + base
	}
	if base != "" {
		return "YC-7ZIP" + base
	}
	return "YC-7ZIP"
}

// ------------------------------------------------------------------ 发送

// send 按渠道类型分发，返回一段"给人看的现场"。
//
// 第二个返回值是成功与否；失败时 detail 一定是能直接显示在界面上的原因
// （HTTP 状态 + 响应片段，或"没填地址"这类本地错误），调用方不必再拼。
func (n *notifyStore) send(ctx context.Context, ch NotifyChannel, msg notifyMessage) (string, bool) {
	ch = n.withSecrets(ch)
	switch ch.Type {
	case notifyWeCom:
		return n.sendWeCom(ctx, ch, msg)
	case notifyDingTalk:
		return n.sendDingTalk(ctx, ch, msg)
	case notifyFeishu:
		return n.sendFeishu(ctx, ch, msg)
	case notifyQQBot:
		return n.sendQQBot(ctx, ch, msg)
	case notifyWebhook:
		return n.sendWebhook(ctx, ch, msg)
	}
	return "不认识的渠道类型：" + ch.Type, false
}

func (n *notifyStore) sendWeCom(ctx context.Context, ch NotifyChannel, msg notifyMessage) (string, bool) {
	if err := checkNotifyURL(ch.URL); err != nil {
		return err.Error(), false
	}
	status, body, err := n.doJSON(ctx, ch.URL, map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": msg.Markdown()},
	}, nil)
	if err != nil {
		return "请求失败：" + err.Error(), false
	}
	detail := detailOf(status, body)
	if !httpOK(status) {
		return detail, false
	}
	if e := platformError(body); e != "" {
		return detail + " · " + e, false
	}
	return detail, true
}

func (n *notifyStore) sendDingTalk(ctx context.Context, ch NotifyChannel, msg notifyMessage) (string, bool) {
	if err := checkNotifyURL(ch.URL); err != nil {
		return err.Error(), false
	}
	status, body, err := n.doJSON(ctx, ch.URL, map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"title": msg.Title, "text": msg.Markdown()},
	}, nil)
	if err != nil {
		return "请求失败：" + err.Error(), false
	}
	detail := detailOf(status, body)
	if !httpOK(status) {
		return detail, false
	}
	if e := platformError(body); e != "" {
		return detail + " · " + e, false
	}
	return detail, true
}

func (n *notifyStore) sendFeishu(ctx context.Context, ch NotifyChannel, msg notifyMessage) (string, bool) {
	if err := checkNotifyURL(ch.URL); err != nil {
		return err.Error(), false
	}
	status, body, err := n.doJSON(ctx, ch.URL, map[string]any{
		"msg_type": "text",
		"content":  map[string]any{"text": msg.PlainText()},
	}, nil)
	if err != nil {
		return "请求失败：" + err.Error(), false
	}
	detail := detailOf(status, body)
	if !httpOK(status) {
		return detail, false
	}
	if e := platformError(body); e != "" {
		return detail + " · " + e, false
	}
	return detail, true
}

func (n *notifyStore) sendWebhook(ctx context.Context, ch NotifyChannel, msg notifyMessage) (string, bool) {
	if err := checkNotifyURL(ch.URL); err != nil {
		return err.Error(), false
	}
	e := msg.Entry
	payload := map[string]any{
		"source":      "YC-7ZIP",
		"version":     msg.Version,
		"event":       msg.Event,
		"title":       msg.Title,
		"text":        msg.PlainText(),
		"job":         e.ID,
		"kind":        e.Kind,
		"status":      e.Status,
		"duration_ms": e.DurationMS,
		"total_bytes": e.TotalBytes,
		"output_dir":  e.OutputDir,
		"output_mode": e.OutputMode,
		"service":     msg.Service,
	}
	if len(e.Sources) > 0 {
		payload["sources"] = e.Sources
	}
	if len(e.Outputs) > 0 {
		names := make([]string, 0, len(e.Outputs))
		for _, f := range e.Outputs {
			names = append(names, f.Name)
		}
		payload["outputs"] = names
	}
	if e.Message != "" {
		payload["error"] = e.Message
	}

	headers := map[string]string{}
	if ch.HeaderName != "" {
		headers[ch.HeaderName] = ch.HeaderValue
	}
	status, body, err := n.doJSON(ctx, ch.URL, payload, headers)
	if err != nil {
		return "请求失败：" + err.Error(), false
	}
	detail := detailOf(status, body)
	if !httpOK(status) {
		return detail, false
	}
	return detail, true
}

// sendQQBot 走 QQ 官方 v2 API：先拿（或复用）access_token，再往群或单聊里发。
func (n *notifyStore) sendQQBot(ctx context.Context, ch NotifyChannel, msg notifyMessage) (string, bool) {
	if ch.AppID == "" || ch.AppSecret == "" {
		return "缺少 AppID 或 AppSecret", false
	}
	if ch.TargetID == "" {
		return "缺少目标 openid", false
	}

	token, detail, ok := n.qqAccessToken(ctx, ch.AppID, ch.AppSecret)
	if !ok {
		return detail, false
	}

	// 官方没有二维码/扫码接入：只有在开放平台注册开发者、创建应用拿到 AppID/AppSecret，
	// 再把机器人加进群或发起单聊，从这里拿到的 openid 发消息。
	path := "/v2/groups/" + url.PathEscape(ch.TargetID) + "/messages"
	if ch.TargetType == "user" {
		path = "/v2/users/" + url.PathEscape(ch.TargetID) + "/messages"
	}

	status, body, err := n.doJSON(ctx, n.apiBase+path, map[string]any{
		"content":  "【" + msg.Title + "】\n" + msg.Body(),
		"msg_type": 0,
	}, map[string]string{"Authorization": "QQBot " + token})
	if err != nil {
		return "请求失败：" + err.Error(), false
	}
	out := detailOf(status, body)
	if !httpOK(status) {
		return out, false
	}
	if e := platformError(body); e != "" {
		return out + " · " + e, false
	}
	return out, true
}

// qqAccessToken 取（或复用）access_token。
//
// 必须缓存：官方 token 有效期约 7200 秒，换取接口还有频控，每条通知都去换一次
// 会被拦，也会把一个本地动作变成两次串行的网络往返。缓存按 AppID 分，密钥变了
// 就作废重取——换了应用还拿着旧 token 会 401。
func (n *notifyStore) qqAccessToken(ctx context.Context, appID, secret string) (string, string, bool) {
	n.tokenMu.Lock()
	if t := n.qqTokens[appID]; t != nil && t.secret == secret && time.Now().Before(t.expiresAt) {
		token := t.token
		n.tokenMu.Unlock()
		return token, "", true
	}
	n.tokenMu.Unlock()

	status, body, err := n.doJSON(ctx, n.tokenURL, map[string]any{
		"appId":        appID,
		"clientSecret": secret,
	}, nil)
	if err != nil {
		return "", "获取 access_token 失败：" + err.Error(), false
	}
	if !httpOK(status) {
		return "", "获取 access_token 失败：" + detailOf(status, body), false
	}

	// expires_in 官方给的是字符串（"7200"），json.Number 两种都收。
	var out struct {
		AccessToken string      `json:"access_token"`
		ExpiresIn   json.Number `json:"expires_in"`
		Message     string      `json:"message"`
	}
	_ = json.Unmarshal(body, &out)
	if out.AccessToken == "" {
		return "", "获取 access_token 失败：" + detailOf(status, body), false
	}

	ttl := qqTokenTTL
	if sec, err := out.ExpiresIn.Int64(); err == nil && sec > 0 {
		ttl = time.Duration(sec) * time.Second
	}
	expires := time.Now().Add(ttl - qqTokenMargin)
	// 富余要留在未来：万一 expires_in 给得很小，也不能存一个立刻就过期的 token。
	if !expires.After(time.Now()) {
		expires = time.Now().Add(time.Minute)
	}

	n.tokenMu.Lock()
	n.qqTokens[appID] = &qqToken{secret: secret, token: out.AccessToken, expiresAt: expires}
	n.tokenMu.Unlock()
	return out.AccessToken, "", true
}

// doJSON 发一个 JSON 请求，返回状态码与响应体（读够判断就行，不把整页报错读进来）。
func (n *notifyStore) doJSON(ctx context.Context, rawURL string, payload any, headers map[string]string) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := n.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	return res.StatusCode, body, nil
}

// detailOf 拼出给界面看的"HTTP 现场"。
func detailOf(status int, body []byte) string {
	// 压掉换行与多余空白：这段文字要显示在设置面板的一行里。
	snippet := strings.Join(strings.Fields(string(body)), " ")
	if snippet == "" {
		return "HTTP " + strconv.Itoa(status)
	}
	return "HTTP " + strconv.Itoa(status) + " · " + truncRunes(snippet, notifySnippet)
}

// platformError 读平台响应体里的错误码。
//
// 企业微信与钉钉用 errcode，飞书与 QQ 用 code，飞书老接口用 StatusCode：
// 都是"HTTP 200 但内部失败"。少了这一步，把 webhook key 填错会显示成发送成功。
// 不是 JSON（例如反代回的一段 HTML）就交给状态码判断。
func platformError(body []byte) string {
	var probe struct {
		ErrCode    *int   `json:"errcode"`
		ErrMsg     string `json:"errmsg"`
		Code       *int   `json:"code"`
		Msg        string `json:"msg"`
		Message    string `json:"message"`
		StatusCode *int   `json:"StatusCode"`
		StatusMsg  string `json:"StatusMessage"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	if probe.ErrCode != nil && *probe.ErrCode != 0 {
		return "errcode=" + strconv.Itoa(*probe.ErrCode) + " " + firstNonEmpty(probe.ErrMsg, probe.Message)
	}
	if probe.StatusCode != nil && *probe.StatusCode != 0 {
		return "code=" + strconv.Itoa(*probe.StatusCode) + " " + firstNonEmpty(probe.StatusMsg, probe.Msg)
	}
	if probe.Code != nil && *probe.Code != 0 {
		return "code=" + strconv.Itoa(*probe.Code) + " " + firstNonEmpty(probe.Msg, probe.Message)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func httpOK(status int) bool { return status >= 200 && status <= 299 }

// checkNotifyURL 只放行 http(s)。
//
// 地址是用户手填的，而 file:// 之类会让这个"发个消息"的能力变成别的用途；
// 空地址则是最常见的漏填，单独给一句话比一个连接错误好懂。
func checkNotifyURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("没有填写 Webhook 地址")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("地址无法解析：" + err.Error())
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("地址必须是 http 或 https")
	}
	if u.Host == "" {
		return errors.New("地址缺少主机名")
	}
	return nil
}

// ---------------------------------------------------------------- 任务挂钩

// notifyJob 在任务收尾时按开关把结果推出去。
//
// 与 recordHistory 共用同一条记录：任务结束时只有这一个地方拿得到终态。
func (s *Server) notifyJob(e HistoryEntry) {
	if s.notifications == nil {
		return
	}
	triggers, channels := s.notifications.active()

	var wanted bool
	switch e.Status {
	case string(job.StatusDone):
		wanted = triggers.Done
	case string(job.StatusError):
		wanted = triggers.Error
	default:
		// 取消不发：那是用户自己点掉的，不需要再被告知一次。
		return
	}
	if !wanted || len(channels) == 0 {
		return
	}

	msg := s.buildNotifyMessage(e)

	// 必须另起 goroutine，而且不能挂在任务上：这时任务已经结束，它的 context
	// 往往已经被取消（失败那次尤其是）。挂在上面就成了"通知拖住任务"，
	// 而通知本来只是顺带说一声。失败只写日志，不写回任务状态。
	go func() {
		for _, ch := range channels {
			ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
			detail, ok := s.notifications.send(ctx, ch, msg)
			cancel()
			if ok {
				s.log.Info("通知已发送", "channel", ch.Name, "type", ch.Type, "job", e.ID)
				continue
			}
			s.log.Warn("通知发送失败", "channel", ch.Name, "type", ch.Type, "job", e.ID, "detail", detail)
		}
	}()
}

// ---------------------------------------------------------------- 处理器

// handleNotifyGet 返回通知配置（密钥已脱敏）。
func (s *Server) handleNotifyGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.notifications.snapshot())
}

// handleNotifyPut 整体替换通知配置：界面就是这么改的，逐条打补丁反而更难对齐。
func (s *Server) handleNotifyPut(w http.ResponseWriter, r *http.Request) {
	var body notifyPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误："+err.Error())
		return
	}
	err := s.notifications.save(notifyFile{
		Enabled:  notifyTriggers{Done: body.Done, Error: body.Error},
		Channels: body.Channels,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存通知设置失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.notifications.snapshot())
}

// handleNotifyTest 用这次填的渠道发一条测试消息。
//
// 试发不要求先保存：地址对不对，往往是填完就想立刻知道的事情。
// 请求体里若带了 id，服务端会把界面没回传的密钥补上（见 withSecrets）。
// 结果一律 200 + ok 字段：这是"测试的结果"，不是这个接口自己失败了。
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	var ch NotifyChannel
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&ch); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误："+err.Error())
		return
	}
	if !isNotifyType(ch.Type) {
		writeError(w, http.StatusBadRequest, "未知的渠道类型")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), notifyTimeout)
	defer cancel()

	detail, ok := s.notifications.send(ctx, ch, s.testMessage())
	if detail == "" {
		detail = "没有收到任何响应"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "detail": detail})
}
