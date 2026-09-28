package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// CSRF 这道闸有几个分支，而它误判过一次：飞牛网关会把 Host 改写成自己的名字，
// 原先拿 Origin 与 Host 硬比，于是网关里"点任何按钮都报拒绝跨站请求"。
// 现在每条分支都钉住。
//
// 用一条不存在的路径来观察结果：闸在路由之前。放行的表现是 405 —— 路径被那个
// 兜底的 `GET /` 模式接住、但方法对不上；被闸拦住才是 403。
func TestCSRFGuard(t *testing.T) {
	cases := []struct {
		name    string
		auth    string
		headers map[string]string
		want    int
	}{
		{
			name:    "浏览器自报跨站",
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"},
			want:    http.StatusForbidden,
		},
		{
			name:    "浏览器自报同源",
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"},
			want:    http.StatusMethodNotAllowed,
		},
		{
			name:    "地址栏直接进来",
			headers: map[string]string{"Sec-Fetch-Site": "none"},
			want:    http.StatusMethodNotAllowed,
		},
		{
			name: "没有 Origin 的脚本请求",
			want: http.StatusMethodNotAllowed,
		},
		{
			// 网关改写 Host，但浏览器自己说了是同源——这正是飞牛应用包的场景
			name: "网关改写 Host 但浏览器自报同源",
			headers: map[string]string{
				"Sec-Fetch-Site": "same-origin",
				"Origin":         "http://nas:5666",
				"Host":           "localhost:8090",
			},
			want: http.StatusMethodNotAllowed,
		},
		{
			// 没配 Basic 认证：闸要防的"浏览器自动带上缓存凭据"不成立，
			// 不该因为代理改了 Host 就误伤
			name: "没配 Basic 认证时不因代理改写 Host 而误拒",
			headers: map[string]string{
				"Origin": "http://nas:5666",
				"Host":   "localhost:8090",
			},
			want: http.StatusMethodNotAllowed,
		},
		{
			// 配了 Basic 认证时，不发 Sec-Fetch-Site 的老浏览器必须走严格比对
			name:    "配了 Basic 认证时拦住跨站请求",
			auth:    "u:p",
			headers: map[string]string{"Origin": "http://evil.example"},
			want:    http.StatusForbidden,
		},
		{
			name: "配了 Basic 认证时同源请求放行",
			auth: "u:p",
			headers: map[string]string{
				"Origin": "http://127.0.0.1:8090",
				"Host":   "127.0.0.1:8090",
			},
			want: http.StatusMethodNotAllowed,
		},
		{
			name: "配了 Basic 认证时认转发头里的原始 Host",
			auth: "u:p",
			headers: map[string]string{
				"Origin":           "http://nas:5666",
				"X-Forwarded-Host": "nas:5666",
				"Host":             "localhost:8090",
			},
			want: http.StatusMethodNotAllowed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newUpdateServer(t, Config{Auth: tc.auth})
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/does-not-exist", nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.headers {
				if k == "Host" {
					req.Host = v
					continue
				}
				req.Header.Set(k, v)
			}
			// 闸放行之后还要过认证那一层，否则拿到的是 401 而不是 404
			if tc.auth != "" {
				req.SetBasicAuth("u", "p")
			}

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("状态码不对：得到 %d，期望 %d", res.StatusCode, tc.want)
			}
		})
	}
}
