package qrcode

import (
	"strings"
	"testing"
)

// ------------------------------------------------------------ 结构

// 三个角的定位图形：7x7 的"回"字，外面一圈浅色分隔带。
func TestFinderPatterns(t *testing.T) {
	m := mustEncode(t, "https://q.qq.com/qqbot/openclaw/connect.html?task_id=abc&_wv=2")
	n := m.Size
	// 三个角上的定位图形贴边，往外看会越界——越界当成白的。
	dark := func(x, y int) bool {
		if x < 0 || y < 0 || x >= n || y >= n {
			return false
		}
		return m.Dark(x, y)
	}
	for _, c := range []struct{ cx, cy int }{{3, 3}, {n - 4, 3}, {3, n - 4}} {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if !dark(c.cx+dx, c.cy+dy) { // 中心 3x3 全黑
					t.Fatalf("定位图形中心区 (%d,%d) 应该是黑的", c.cx+dx, c.cy+dy)
				}
			}
		}
		for _, d := range []int{-2, 2} {
			if dark(c.cx+d, c.cy) || dark(c.cx, c.cy+d) {
				t.Fatalf("定位图形第 2 圈 (%d,%d) 应该是白的", c.cx+d, c.cy)
			}
		}
		if !dark(c.cx+3, c.cy) || !dark(c.cx, c.cy+3) {
			t.Fatal("定位图形第 3 圈应该是黑的（外框）")
		}
		if dark(c.cx+4, c.cy) || dark(c.cx, c.cy+4) {
			t.Fatal("定位图形外的分隔带应该是白的")
		}
	}
}

// 时序图形黑白交替；右下角的固定黑模块必须在。
func TestTimingAndDarkModule(t *testing.T) {
	m := mustEncode(t, "timing")
	n := m.Size
	for i := 8; i < n-8; i++ {
		want := i%2 == 0
		if m.Dark(i, 6) != want {
			t.Fatalf("第 6 行第 %d 列的时序模块不对", i)
		}
		if m.Dark(6, i) != want {
			t.Fatalf("第 6 列第 %d 行的时序模块不对", i)
		}
	}
	if !m.Dark(8, n-8) {
		t.Fatal("固定的黑模块 (8, size-8) 必须在")
	}
}

// 校正图形只在版本 2 以上出现，且三个定位图形的位置上不画。
func TestAlignmentPatterns(t *testing.T) {
	cases := []struct {
		text string
		ver  int
		pos  []int
	}{
		{"x", 1, nil},
		{strings.Repeat("a", 26), 2, []int{6, 18}},
		{strings.Repeat("a", 122), 7, []int{6, 22, 38}},
	}
	for _, c := range cases {
		m := mustEncode(t, c.text)
		if m.Version != c.ver {
			t.Fatalf("%d 字节应编成版本 %d，得到 %d", len(c.text), c.ver, m.Version)
		}
		n := m.Size
		for _, cy := range c.pos {
			for _, cx := range c.pos {
				corner := (cx == 6 && cy == 6) || (cx == 6 && cy == n-7) || (cx == n-7 && cy == 6)
				darkCenter := m.Dark(cx, cy)
				if corner {
					continue
				}
				if !darkCenter {
					t.Fatalf("校正图形中心 (%d,%d) 应该是黑的", cx, cy)
				}
				if m.Dark(cx+1, cy) {
					t.Fatalf("校正图形第 2 圈 (%d,%d) 应该是白的", cx+1, cy)
				}
			}
		}
	}
}

// ------------------------------------------------------------ 版本

func TestPickVersionCoversCapacity(t *testing.T) {
	// 每个版本都正好能装下它的容量，多一个字节就要换下一个版本。
	for ver := 1; ver <= maxVersion; ver++ {
		cap := byteCapacity(ver)
		if got := pickVersion(cap); got != ver {
			t.Fatalf("%d 字节应选版本 %d，得到 %d", cap, ver, got)
		}
		if ver < maxVersion && byteCapacity(ver+1) != cap {
			if got := pickVersion(cap + 1); got != ver+1 {
				t.Fatalf("%d 字节应选版本 %d，得到 %d", cap+1, ver+1, got)
			}
		}
	}
	if pickVersion(byteCapacity(maxVersion)+1) != 0 {
		t.Fatal("超出容量应返回 0，而不是悄悄编出一个装不下的码")
	}
	if _, err := Encode(strings.Repeat("a", byteCapacity(maxVersion)+1)); err == nil {
		t.Fatal("超出容量应报错")
	}
}

func TestSizeFollowsVersion(t *testing.T) {
	m := mustEncode(t, "x")
	if m.Size != 21 || m.Version != 1 {
		t.Fatalf("1 个字节应是版本 1 / 21 模块，得到版本 %d / %d", m.Version, m.Size)
	}
}

// ------------------------------------------------------------ 格式与版本信息

// 格式信息要能被读回来：纠错等级 M、以及实际用的掩码。
func TestFormatInfoReadsBack(t *testing.T) {
	for _, text := range []string{"x", strings.Repeat("b", 30), strings.Repeat("c", 200)} {
		m := mustEncode(t, text)
		// 5 位数据段 = 2 位纠错等级 + 3 位掩码
		got := decodeFormat(t, m)
		if got>>3 != 0 {
			t.Fatalf("纠错等级应该是 M（00），得到 %02b", got>>3)
		}
		if got&7 != m.Mask {
			t.Fatalf("格式信息里的掩码 %d 与实际用的 %d 对不上", got&7, m.Mask)
		}
	}
}

// 版本 7 起才有 18 位的版本信息，两份副本都要一致。
func TestVersionInfoReadsBack(t *testing.T) {
	m := mustEncode(t, strings.Repeat("d", 130))
	if m.Version < 7 {
		t.Fatalf("130 字节应编成版本 7 以上，得到 %d", m.Version)
	}
	n := m.Size
	read := func(a, b int, swap bool) int {
		v := 0
		for i := 0; i < 18; i++ {
			x, y := n-11+i%3, i/3
			if swap {
				x, y = y, x
			}
			if m.Dark(x, y) {
				v |= 1 << uint(i)
			}
		}
		return v
	}
	a, b := read(0, 0, false), read(0, 0, true)
	if a != b {
		t.Fatalf("版本信息的两份副本不一致：%d / %d", a, b)
	}
	// 去掉 12 位 BCH 后就是版本号
	if got := a >> 12; got != m.Version {
		t.Fatalf("版本信息里写的是 %d，实际是 %d", got, m.Version)
	}
}

// ------------------------------------------------------------ 数据可读回

// 把矩阵反读回来（解掩码 → 蛇形取码字 → 反交错 → 解字节模式），
// 结果必须与原文一字不差。这条覆盖了掩码、排布、分块与交错。
func TestPayloadRoundTrips(t *testing.T) {
	cases := []string{
		"x",
		"https://q.qq.com/qqbot/openclaw/connect.html?task_id=abc123&_wv=2",
		strings.Repeat("a", byteCapacity(5)),
		strings.Repeat("b", byteCapacity(8)),
		strings.Repeat("c", byteCapacity(maxVersion)),
		"中文也要能编进去：扫码绑定",
	}
	for _, text := range cases {
		m := mustEncode(t, text)
		got := decodePayload(t, m)
		if got != text {
			t.Fatalf("反读回来的内容不对（%d 字节）：%q", len(text), got)
		}
	}
}

// ------------------------------------------------------------ 与参考实现对拍

// 这两组是 node-qrcode v1.5.4（现代实现，纠错等级 M）的输出，逐位相同。
// 钉住的是"能不能被别人的扫描器读出来"这件事里最容易错的部分：
// 码字、纠错、交错与排布。掩码选择那点差异见 penalty 的注释。
func TestGoldenMatrices(t *testing.T) {
	cases := []struct {
		text string
		rows []string
	}{
		{
			text: "https://q.qq.com/qqbot/ope",
			rows: []string{
				"1111111000001010101111111",
				"1000001000010001101000001",
				"1011101011000010101011101",
				"1011101011011110001011101",
				"1011101011001010001011101",
				"1000001010101001101000001",
				"1111111010101010101111111",
				"0000000010000011100000000",
				"1011111000101110001111100",
				"0010010011011000100000010",
				"0010011111101101110101011",
				"0000100100011000100000001",
				"1001101100101110011110111",
				"1010100101001100110101010",
				"1010101011100111101111011",
				"1001100001100010011110001",
				"1011001000101111111110100",
				"0000000010110011100011000",
				"1111111000101010101010111",
				"1000001010001000100011000",
				"1011101011000101111110100",
				"1011101011110101111011111",
				"1011101010100110110001101",
				"1000001001010011101111001",
				"1111111011111111011111111",
			},
		},
		{
			text: "https://q.qq.com/qqbot/openclaw/connect.html?task_id=abc123def456&source=yc7zip&_wv=2",
			rows: []string{
				"11111110000111010111111111001001001111111",
				"10000010000011001100010001111010001000001",
				"10111010101100100111011001000001101011101",
				"10111010111000111001010000110010001011101",
				"10111010111110000111111100001001101011101",
				"10000010101010010100110001010010001000001",
				"11111110101010101010101010101010101111111",
				"00000000101101010001111100101001100000000",
				"10111110011011101101001101100111101111100",
				"10000101100011110101110101001111101010101",
				"10001111111100011010111000010100110000000",
				"01110101101001100001110010001011111011001",
				"10111011000000111101101011101101010001101",
				"11011000111001001110000001110010110010011",
				"11111111100111011010111111111001010101100",
				"11000000010110110000011100110011101111000",
				"00001110111010011100101111111111010100110",
				"11111100100101101001101100101101101110011",
				"00001011011001011010101000010100101010100",
				"10101100100001000001100110101110101011011",
				"00101111110011011010111000010000000100101",
				"01010100001111110000110111100111111011011",
				"11010110011100000010100011110100110111100",
				"01100000011101111011010000111011101101000",
				"10010111100010000101001101110101000101100",
				"01011100111011011011010100000001011011000",
				"00000110101110110000010000111000101110000",
				"10001100001110100010111100010001111011011",
				"00111110101111101110001101110101101101100",
				"10101100101010000000101010011010010111111",
				"10001110110101100011010000100000111110000",
				"10000101111001010011111010101000011011000",
				"10101010110010001111101011100110111110101",
				"00000000101100011111010101100000100010011",
				"11111110011011011100110011110011101011100",
				"10000010110101001111011111001001100010000",
				"10111010110100101000010010111010111111100",
				"10111010100000101111101101001000010101000",
				"10111010111001010110101000111011011110100",
				"10000010001001110011011000100000110010010",
				"11111110100000101110101111011101001111100",
			},
		},
	}
	for _, c := range cases {
		m := mustEncode(t, c.text)
		rows := m.Rows()
		if len(rows) != len(c.rows) {
			t.Fatalf("尺寸对不上：%d vs %d", len(rows), len(c.rows))
		}
		for i := range rows {
			if rows[i] != c.rows[i] {
				t.Fatalf("第 %d 行与参考实现不同：\n得到 %s\n期望 %s", i, rows[i], c.rows[i])
			}
		}
	}
}

// ------------------------------------------------------------ SVG

func TestSVG(t *testing.T) {
	svg, err := SVG("https://example.com/x", 8)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(svg, "<svg ") || !strings.HasSuffix(svg, "</svg>") {
		t.Fatalf("不像一段 SVG：%.60s…", svg)
	}
	// 画布 = 模块数 + 两侧各 4 个模块的白边
	if !strings.Contains(svg, "viewBox=\"0 0 33 33\"") {
		t.Fatalf("静区没算对：%s", svg[:120])
	}
	m := mustEncode(t, "https://example.com/x")
	if got := strings.Count(svg, "M"); got == 0 || got > m.Size*m.Size {
		t.Fatalf("path 的段数不合理：%d", got)
	}
	if _, err := SVG(strings.Repeat("a", 5000), 8); err == nil {
		t.Fatal("超长内容应报错，而不是画一张空图")
	}
	// 默认模块尺寸
	if small, _ := SVG("x", 0); !strings.Contains(small, "width=\"232\"") {
		t.Fatalf("默认模块尺寸应为 8px：%s", small[:120])
	}
}

func mustEncode(t *testing.T, text string) *Matrix {
	t.Helper()
	m, err := Encode(text)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ------------------------------------------------------------ 反读工具（仅测试用）

// functionModules 标出功能模块（定位、分隔、时序、校正、格式、版本信息）。
func functionModules(m *Matrix) []bool {
	n := m.Size
	f := make([]bool, n*n)
	mark := func(x, y int) {
		if x >= 0 && y >= 0 && x < n && y < n {
			f[y*n+x] = true
		}
	}
	for i := 0; i < n; i++ {
		mark(6, i)
		mark(i, 6)
	}
	for _, c := range [][2]int{{3, 3}, {n - 4, 3}, {3, n - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				mark(c[0]+dx, c[1]+dy)
			}
		}
	}
	for i := 0; i < 8; i++ { // 格式信息的两份副本
		mark(8, i)
		mark(i, 8)
		mark(n-1-i, 8)
		mark(8, n-1-i)
	}
	mark(8, n-8)
	ver := m.Version
	if ver >= 7 { // 版本信息的两份副本
		for i := 0; i < 18; i++ {
			a, b := n-11+i%3, i/3
			mark(a, b)
			mark(b, a)
		}
	}
	// 校正图形：按版本的实际中心坐标标出，跳过三个定位图形所在地
	pos := alignPositions[ver-1]
	for _, cy := range pos {
		for _, cx := range pos {
			if (cx == 6 && cy == 6) || (cx == 6 && cy == n-7) || (cx == n-7 && cy == 6) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					mark(cx+dx, cy+dy)
				}
			}
		}
	}
	return f
}

// rawCodewords 解掩码后按规范的蛇形顺序取出码字。
func rawCodewords(t *testing.T, m *Matrix) []byte {
	t.Helper()
	n := m.Size
	funcs := functionModules(m)
	bits := make([]bool, 0, n*n)
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < n; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				upward := (right+1)&2 == 0
				y := vert
				if upward {
					y = n - 1 - vert
				}
				if funcs[y*n+x] {
					continue
				}
				v := m.Dark(x, y)
				if maskBit(m.Mask, x, y) {
					v = !v
				}
				bits = append(bits, v)
			}
		}
	}
	out := make([]byte, len(bits)/8)
	for i, b := range bits {
		if b {
			out[i>>3] |= 1 << uint(7-(i&7))
		}
	}
	return out
}

// maskBit 是规范里的八种掩码，x 是列、y 是行。
func maskBit(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	default:
		return ((x+y)%2+x*y%3)%2 == 0
	}
}

// dataCodewordsAntiInterleaved 把码字流反交错回"按块顺序的数据码字"。
func dataCodewordsAntiInterleaved(t *testing.T, m *Matrix, cw []byte) []byte {
	t.Helper()
	ver := m.Version
	numBlocks := numBlocksM[ver-1]
	eccLen := eccPerBlockM[ver-1]
	shortLen := len(cw) / numBlocks
	numShort := numBlocks - len(cw)%numBlocks

	datas := make([][]byte, numBlocks)
	for i := 0; i < numBlocks; i++ {
		datLen := shortLen - eccLen
		if i >= numShort {
			datLen++
		}
		datas[i] = make([]byte, datLen)
	}
	idx := 0
	maxData := shortLen - eccLen
	if numShort < numBlocks {
		maxData++
	}
	for i := 0; i < maxData; i++ {
		for b := 0; b < numBlocks; b++ {
			if i < len(datas[b]) {
				datas[b][i] = cw[idx]
				idx++
			}
		}
	}
	out := make([]byte, 0, dataCodewords(ver))
	for _, d := range datas {
		out = append(out, d...)
	}
	return out
}

// decodeFormat 读回纠错等级与掩码（第一份副本）。
func decodeFormat(t *testing.T, m *Matrix) int {
	t.Helper()
	bit := func(x, y int) int {
		if m.Dark(x, y) {
			return 1
		}
		return 0
	}
	v := 0
	for i := 0; i <= 5; i++ {
		v |= bit(8, i) << uint(i)
	}
	v |= bit(8, 7) << 6
	v |= bit(8, 8) << 7
	v |= bit(7, 8) << 8
	for i := 9; i < 15; i++ {
		v |= bit(14-i, 8) << uint(i)
	}
	return (v ^ 0x5412) >> 10
}

// decodePayload 反读出字节模式的内容。
func decodePayload(t *testing.T, m *Matrix) string {
	t.Helper()
	raw := rawCodewords(t, m)
	data := dataCodewordsAntiInterleaved(t, m, raw)
	bits := make([]bool, 0, len(data)*8)
	for _, b := range data {
		for i := 7; i >= 0; i-- {
			bits = append(bits, (b>>uint(i))&1 != 0)
		}
	}
	read := func(from, n int) int {
		v := 0
		for i := 0; i < n; i++ {
			v <<= 1
			if bits[from+i] {
				v |= 1
			}
		}
		return v
	}
	if mode := read(0, 4); mode != 0b0100 {
		t.Fatalf("模式应为字节模式，得到 %04b", mode)
	}
	ccBits := 8
	if m.Version >= 10 {
		ccBits = 16
	}
	length := read(4, ccBits)
	out := make([]byte, 0, length)
	pos := 4 + ccBits
	for i := 0; i < length; i++ {
		out = append(out, byte(read(pos, 8)))
		pos += 8
	}
	return string(out)
}
