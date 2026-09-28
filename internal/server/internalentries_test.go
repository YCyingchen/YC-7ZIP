package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 飞牛自己的目录不该出现在文件列表里：列出来只会让人在压缩包之间找路时踩进
// 系统目录（@appcenter、@appdata、docker、thumb…），而这个工具在那里没事可做。
func TestInternalEntriesAreInternal(t *testing.T) {
	cases := []struct {
		name   string
		parent string
		want   bool
	}{
		{name: "@appcenter", parent: "/vol1", want: true},
		{name: "@appdata", parent: "/vol1", want: true},
		{name: "@sysappmeta", parent: "/", want: true},
		{name: "appcenter-downloads", parent: "/vol1", want: true},
		{name: "mediasrv.transcode", parent: "/vol5", want: true},
		{name: "thumb", parent: "/vol1", want: true},
		{name: "lost+found", parent: "/vol5", want: true},

		// 卷根上的这三个是飞牛自己的
		{name: "docker", parent: "/vol1", want: true},
		{name: "vm", parent: "/vol1", want: true},
		{name: "fs", parent: "/vol1", want: true},
		// 但用户自己的同名目录在 /volN/<uid>/ 里，不能连它一起藏
		{name: "docker", parent: "/vol1/1000", want: false},
		{name: "vm", parent: "/vol5/1000/空间4", want: false},
		// 容器部署里应用的根就是容器，/share 这类挂载点要留着
		{name: "docker", parent: "/", want: false},
		{name: "share", parent: "/", want: false},
		{name: "data", parent: "/", want: false},
		// 根目录下操作系统自己的目录
		{name: "etc", parent: "/", want: true},
		{name: "proc", parent: "/", want: true},
		{name: "usr", parent: "/", want: true},
		{name: "vol1", parent: "/", want: false},

		// 普通用户目录
		{name: "1000", parent: "/vol1", want: false},
		{name: "空间4", parent: "/vol5/1000", want: false},
		{name: "导航页", parent: "/vol1/1000", want: false},
	}
	for _, tc := range cases {
		if got := isInternalEntry(tc.name, tc.parent); got != tc.want {
			t.Errorf("isInternalEntry(%q, %q) = %v，期望 %v", tc.name, tc.parent, got, tc.want)
		}
	}
}

// 端到端：列表里不该出现系统目录，但权限不受影响（那只是"看不见"）。
func TestBrowseHidesInternalEntries(t *testing.T) {
	shared := t.TempDir()
	for _, name := range []string{"1000", "@appdata", "thumb", "lost+found", "用户的压缩包"} {
		if err := os.MkdirAll(filepath.Join(shared, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	srv := newUpdateServer(t, Config{AllowRoots: []string{shared}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/browse?path=" + shared)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Entries []browseEntry `json:"entries"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, e := range body.Entries {
		names = append(names, e.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "1000") || !strings.Contains(joined, "用户的压缩包") {
		t.Fatalf("用户目录应当照常列出：%s", joined)
	}
	for _, hidden := range []string{"@appdata", "thumb", "lost+found"} {
		if strings.Contains(joined, hidden) {
			t.Fatalf("%s 不该出现：%s", hidden, joined)
		}
	}
}
