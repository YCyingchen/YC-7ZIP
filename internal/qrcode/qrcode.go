// Package qrcode 是一个最小实现的 QR 码编码器。
//
// 为什么不引第三方库：这个项目的 go.mod 里只有 module 与 go 版本两行，而二维码
// 只用在一个地方——QQ 官方机器人扫码绑定的那张图。为一张图引进依赖，等于把
// "不联网也能跑、源码可通读"这两条一起让出去。
//
// 只实现用得到的那部分：
//   - 字节模式（8-bit），不实现数字/字母数字/汉字模式；
//   - 纠错等级 M（绑定链接是给人扫的屏幕图，不需要 H 的冗余）；
//   - 版本 1–10。QQ 的绑定链接在 80 个字符上下，版本 6/7 就够，留到 10 是余量。
//
// 掩码按规范算四种罚分、取最低的那一个，而不是写死 0 号掩码：掩码选错的码
// 常常"还能扫"，但在光线不好、屏幕反光、拍糊了的时候先坏掉——这正是最不该
// 省的地方，而这部分代码本来也不长。
package qrcode

import (
	"fmt"
	"math"
	"strings"
)

// maxVersion 是本实现支持的最高版本。
const maxVersion = 10

// eccPerBlockM / numBlocksM 是纠错等级 M 下每个版本的纠错码字数与分块数
// （ISO/IEC 18004 表 9）。下标 0 对应版本 1。
var (
	eccPerBlockM = [maxVersion]int{10, 16, 26, 18, 24, 16, 18, 22, 22, 26}
	numBlocksM   = [maxVersion]int{1, 1, 1, 2, 2, 4, 4, 4, 5, 5}
)

// alignPositions 是每个版本校正图形的中心坐标（同一行与同一列共用）。
var alignPositions = [maxVersion][]int{
	{},
	{6, 18},
	{6, 22},
	{6, 26},
	{6, 30},
	{6, 34},
	{6, 22, 38},
	{6, 24, 42},
	{6, 26, 46},
	{6, 28, 50},
}

// Matrix 是编好的模块矩阵，true 表示黑。
type Matrix struct {
	Version int
	Mask    int
	Size    int
	// Modules 行优先，长度 Size*Size。
	Modules []bool
}

// Dark 报告 (x, y) 处是否为黑，x 是列、y 是行。
func (m *Matrix) Dark(x, y int) bool { return m.Modules[y*m.Size+x] }

// Rows 把矩阵按行摊平成 "0101..." 这样的字符串，测试与对拍用。
func (m *Matrix) Rows() []string {
	out := make([]string, 0, m.Size)
	for y := 0; y < m.Size; y++ {
		var b strings.Builder
		for x := 0; x < m.Size; x++ {
			if m.Dark(x, y) {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		out = append(out, b.String())
	}
	return out
}

// Encode 把 text 编成二维码。文本按 UTF-8 字节放进字节模式。
func Encode(text string) (*Matrix, error) {
	data := []byte(text)
	ver := pickVersion(len(data))
	if ver == 0 {
		return nil, fmt.Errorf("内容太长：%d 字节，本实现最多 %d 字节", len(data), byteCapacity(maxVersion))
	}

	code := encodeCodewords(data, ver)
	m := newMatrix(ver)
	m.drawFunctionPatterns(ver)
	m.drawData(code)

	// 八种掩码各试一遍，取罚分最低的那个。
	bestMask, bestPenalty := 0, math.MaxInt
	for mask := 0; mask < 8; mask++ {
		m.applyMask(mask)
		m.drawFormat(mask)
		if p := m.penalty(); p < bestPenalty {
			bestMask, bestPenalty = mask, p
		}
		m.applyMask(mask) // 撤销，下一轮从原样开始
	}
	m.applyMask(bestMask)
	m.drawFormat(bestMask)

	return &Matrix{Version: ver, Mask: bestMask, Size: m.size, Modules: m.dark}, nil
}

// SVG 把 text 编成一张二维码 SVG 字符串。
//
// 输出 SVG 而不是 PNG：SVG 就是一段文本，PNG 还得自己写像素编码与 zlib；
// 而且矢量图放到多大都清楚，设置面板里那点尺寸完全够用。
// modulePixels 是每个模块的像素边长（决定 SVG 的 width/height）。
func SVG(text string, modulePixels int) (string, error) {
	m, err := Encode(text)
	if err != nil {
		return "", err
	}
	if modulePixels <= 0 {
		modulePixels = 8
	}
	// quiet zone：四周各留 4 个模块的白边。少了它，有的扫码器会找不到边界。
	const quiet = 4
	n := m.Size + quiet*2

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges" role="img" aria-label="QR">`,
		n, n, n*modulePixels, n*modulePixels)
	b.WriteString(`<rect width="100%" height="100%" fill="#fff"/>`)
	b.WriteString(`<path d="`)
	// 每一行里连续的黑模块合成一段 path：比"一个模块一个 <rect>"小得多，
	// 浏览器解析起来也快。
	for y := 0; y < m.Size; y++ {
		for x := 0; x < m.Size; {
			if !m.Dark(x, y) {
				x++
				continue
			}
			run := 1
			for x+run < m.Size && m.Dark(x+run, y) {
				run++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", x+quiet, y+quiet, run, run)
			x += run
		}
	}
	b.WriteString(`" fill="#000"/></svg>`)
	return b.String(), nil
}

// ------------------------------------------------------------ 版本与容量

// numRawDataModules 是某个版本能放数据的模块数（含纠错码），按规范算。
func numRawDataModules(ver int) int {
	result := (16*ver+128)*ver + 64
	if ver >= 2 {
		numAlign := ver/7 + 2
		result -= (25*numAlign-10)*numAlign - 55
		if ver >= 7 {
			result -= 36
		}
	}
	return result
}

// dataCodewords 是某个版本里数据码字（不含纠错）的字数。
func dataCodewords(ver int) int {
	return numRawDataModules(ver)/8 - eccPerBlockM[ver-1]*numBlocksM[ver-1]
}

// byteCapacity 是某个版本在字节模式下能放的字节数。
//
// 结束符可以少写几位甚至不写，所以这里不减它；装不下就换下一个版本。
func byteCapacity(ver int) int {
	ccBits := 8
	if ver >= 10 {
		ccBits = 16
	}
	return (dataCodewords(ver)*8 - 4 - ccBits) / 8
}

// pickVersion 选一个刚好装得下的最小版本，装不下返回 0。
func pickVersion(n int) int {
	for ver := 1; ver <= maxVersion; ver++ {
		if n <= byteCapacity(ver) {
			return ver
		}
	}
	return 0
}

// ------------------------------------------------------------ 码字

type bitBuffer struct {
	bits []bool
}

func (b *bitBuffer) len() int { return len(b.bits) }

// append 把 value 的低 n 位按高位在前追加进去。
func (b *bitBuffer) append(value uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		b.bits = append(b.bits, (value>>uint(i))&1 != 0)
	}
}

func (b *bitBuffer) bytes() []byte {
	out := make([]byte, len(b.bits)/8)
	for i, bit := range b.bits {
		if bit {
			out[i>>3] |= 1 << uint(7-(i&7))
		}
	}
	return out
}

// encodeCodewords 生成"数据 + 纠错"并按规范交错好的完整码字序列。
func encodeCodewords(data []byte, ver int) []byte {
	ccBits := 8
	if ver >= 10 {
		ccBits = 16
	}
	capacity := dataCodewords(ver) * 8

	buf := &bitBuffer{}
	buf.append(0b0100, 4) // 字节模式
	buf.append(uint32(len(data)), ccBits)
	for _, b := range data {
		buf.append(uint32(b), 8)
	}

	// 结束符最多 4 个 0；剩下的位置不够 4 位就直接截断（规范允许）。
	term := 4
	if rem := capacity - buf.len(); rem < term {
		term = rem
	}
	buf.append(0, term)
	// 补到字节边界
	if rem := buf.len() % 8; rem != 0 {
		buf.append(0, 8-rem)
	}
	// 交替补 0xEC / 0x11 填满，这两个值在规范里选得让码看起来"花"一点
	for pad := byte(0xEC); buf.len() < capacity; pad ^= 0xEC ^ 0x11 {
		buf.append(uint32(pad), 8)
	}

	return interleaveWithECC(buf.bytes(), ver)
}

// interleaveWithECC 分块算 RS 纠错码，再按规范交错。
//
// 交错分两段，顺序不能混：先把各块的数据码字按列交错（长块比短块多一个数据
// 码字，多出来的那个落在数据段末尾），再把各块的纠错码字按列交错。
// 这里差一个字节，整串码字就全体错位——二维码照样画得出来，但扫出来是乱的。
func interleaveWithECC(data []byte, ver int) []byte {
	numBlocks := numBlocksM[ver-1]
	eccLen := eccPerBlockM[ver-1]
	raw := numRawDataModules(ver) / 8
	numShort := numBlocks - raw%numBlocks
	shortLen := raw / numBlocks

	divisor := rsDivisor(eccLen)
	datas := make([][]byte, numBlocks)
	eccs := make([][]byte, numBlocks)
	offset := 0
	for i := 0; i < numBlocks; i++ {
		datLen := shortLen - eccLen
		if i >= numShort {
			datLen++
		}
		dat := make([]byte, datLen)
		copy(dat, data[offset:offset+datLen])
		offset += datLen
		datas[i] = dat
		eccs[i] = rsRemainder(dat, divisor)
	}

	out := make([]byte, 0, raw)
	maxData := shortLen - eccLen
	if numShort < numBlocks {
		maxData++
	}
	for i := 0; i < maxData; i++ {
		for b := 0; b < numBlocks; b++ {
			if i < len(datas[b]) {
				out = append(out, datas[b][i])
			}
		}
	}
	for i := 0; i < eccLen; i++ {
		for b := 0; b < numBlocks; b++ {
			out = append(out, eccs[b][i])
		}
	}
	return out
}

// rsDivisor 生成 (x - r^0)(x - r^1)...(x - r^(degree-1)) 的系数。
func rsDivisor(degree int) []byte {
	result := make([]byte, degree)
	result[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := range result {
			result[j] = gfMul(result[j], root)
			if j+1 < len(result) {
				result[j] ^= result[j+1]
			}
		}
		root = gfMul(root, 0x02)
	}
	return result
}

// rsRemainder 是 data 除以 divisor 的余数，也就是纠错码字。
func rsRemainder(data, divisor []byte) []byte {
	result := make([]byte, len(divisor))
	for _, b := range data {
		factor := b ^ result[0]
		copy(result, result[1:])
		result[len(result)-1] = 0
		for i, d := range divisor {
			result[i] ^= gfMul(d, factor)
		}
	}
	return result
}

// gfMul 是 GF(256) 上的乘法，本原多项式 0x11D（QR 码规定的那一个）。
func gfMul(x, y byte) byte {
	z := 0
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		if (y>>uint(i))&1 != 0 {
			z ^= int(x)
		}
	}
	return byte(z)
}

// ------------------------------------------------------------ 矩阵

type matrix struct {
	size   int
	dark   []bool
	isFunc []bool
}

func newMatrix(ver int) *matrix {
	size := ver*4 + 17
	return &matrix{size: size, dark: make([]bool, size*size), isFunc: make([]bool, size*size)}
}

// setFunc 写一个功能模块（定位、校正、时序、格式、版本信息）。
// 功能模块不参与掩码，所以这张表必须准确。
func (m *matrix) setFunc(x, y int, dark bool) {
	if x < 0 || y < 0 || x >= m.size || y >= m.size {
		return
	}
	m.dark[y*m.size+x] = dark
	m.isFunc[y*m.size+x] = true
}

func (m *matrix) drawFunctionPatterns(ver int) {
	// 时序图形：第 6 行与第 6 列黑白交替
	for i := 0; i < m.size; i++ {
		m.setFunc(6, i, i%2 == 0)
		m.setFunc(i, 6, i%2 == 0)
	}
	// 三个角的定位图形
	m.drawFinder(3, 3)
	m.drawFinder(m.size-4, 3)
	m.drawFinder(3, m.size-4)
	// 校正图形：跳过三个定位图形所在的位置
	pos := alignPositions[ver-1]
	for _, y := range pos {
		for _, x := range pos {
			if (x == 6 && y == 6) || (x == 6 && y == m.size-7) || (x == m.size-7 && y == 6) {
				continue
			}
			m.drawAlignment(x, y)
		}
	}
	// 先按掩码 0 把格式信息的位置占下来（功能模块），选定掩码后再重画
	m.drawFormat(0)
	m.drawVersion(ver)
}

func (m *matrix) drawFinder(cx, cy int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			dist := max(abs(dx), abs(dy))
			m.setFunc(cx+dx, cy+dy, dist != 2 && dist != 4)
		}
	}
}

func (m *matrix) drawAlignment(cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			m.setFunc(cx+dx, cy+dy, max(abs(dx), abs(dy)) != 1)
		}
	}
}

// drawFormat 写纠错等级与掩码信息的两份副本（BCH(15,5) + 固定掩码 0x5412）。
func (m *matrix) drawFormat(mask int) {
	// 纠错等级 M 的两位是 00，所以数据位就是掩码号本身。
	data := mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return (bits>>uint(i))&1 != 0 }

	for i := 0; i <= 5; i++ {
		m.setFunc(8, i, bit(i))
	}
	m.setFunc(8, 7, bit(6))
	m.setFunc(8, 8, bit(7))
	m.setFunc(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		m.setFunc(14-i, 8, bit(i))
	}

	for i := 0; i < 8; i++ {
		m.setFunc(m.size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		m.setFunc(8, m.size-15+i, bit(i))
	}
	m.setFunc(8, m.size-8, true) // 固定的黑模块
}

// drawVersion 写版本信息（版本 7 起才有，两份副本）。
func (m *matrix) drawVersion(ver int) {
	if ver < 7 {
		return
	}
	rem := ver
	for i := 0; i < 12; i++ {
		rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
	}
	bits := ver<<12 | rem
	for i := 0; i < 18; i++ {
		bit := (bits>>uint(i))&1 != 0
		a, b := m.size-11+i%3, i/3
		m.setFunc(a, b, bit)
		m.setFunc(b, a, bit)
	}
}

// drawData 把码字按规范的"从右下角起、蛇形上下走"填进非功能模块。
func (m *matrix) drawData(code []byte) {
	i := 0
	for right := m.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5 // 跳过第 6 列（时序图形）
		}
		for vert := 0; vert < m.size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				upward := (right+1)&2 == 0
				y := vert
				if upward {
					y = m.size - 1 - vert
				}
				idx := y*m.size + x
				if m.isFunc[idx] || i >= len(code)*8 {
					continue
				}
				m.dark[idx] = (code[i>>3]>>uint(7-(i&7)))&1 != 0
				i++
			}
		}
	}
}

// applyMask 把掩码异或上去；再调一次就是撤销。
func (m *matrix) applyMask(mask int) {
	for y := 0; y < m.size; y++ {
		for x := 0; x < m.size; x++ {
			idx := y*m.size + x
			if m.isFunc[idx] {
				continue
			}
			var invert bool
			switch mask {
			case 0:
				invert = (x+y)%2 == 0
			case 1:
				invert = y%2 == 0
			case 2:
				invert = x%3 == 0
			case 3:
				invert = (x+y)%3 == 0
			case 4:
				invert = (x/3+y/2)%2 == 0
			case 5:
				invert = x*y%2+x*y%3 == 0
			case 6:
				invert = (x*y%2+x*y%3)%2 == 0
			case 7:
				invert = ((x+y)%2+x*y%3)%2 == 0
			}
			if invert {
				m.dark[idx] = !m.dark[idx]
			}
		}
	}
}

// 罚分权重（规范表 11）。
const (
	penaltyN1 = 3
	penaltyN2 = 3
	penaltyN3 = 40
	penaltyN4 = 10
)

// penalty 算四种罚分之和，取最低者即"最好扫"的掩码。
//
// 规则 3（1:1:3:1:1 的定位图形样式）要连着两侧各 4 个浅色模块一起看，
// 所以得维护最近几段游程的长度，而不是简单数连续同色。
func (m *matrix) penalty() int {
	result := 0

	// 规则 1 与 3：逐行
	for y := 0; y < m.size; y++ {
		runColor := false
		runLen := 0
		history := [7]int{}
		for x := 0; x < m.size; x++ {
			if m.dark[y*m.size+x] == runColor {
				runLen++
				if runLen == 5 {
					result += penaltyN1
				} else if runLen > 5 {
					result++
				}
			} else {
				addRun(runLen, &history, m.size)
				if !runColor {
					result += countFinderPatterns(&history) * penaltyN3
				}
				runColor = m.dark[y*m.size+x]
				runLen = 1
			}
		}
		result += terminateRun(runColor, runLen, &history, m.size) * penaltyN3
	}

	// 规则 1 与 3：逐列
	for x := 0; x < m.size; x++ {
		runColor := false
		runLen := 0
		history := [7]int{}
		for y := 0; y < m.size; y++ {
			if m.dark[y*m.size+x] == runColor {
				runLen++
				if runLen == 5 {
					result += penaltyN1
				} else if runLen > 5 {
					result++
				}
			} else {
				addRun(runLen, &history, m.size)
				if !runColor {
					result += countFinderPatterns(&history) * penaltyN3
				}
				runColor = m.dark[y*m.size+x]
				runLen = 1
			}
		}
		result += terminateRun(runColor, runLen, &history, m.size) * penaltyN3
	}

	// 规则 2：2x2 的同色方块
	for y := 0; y < m.size-1; y++ {
		for x := 0; x < m.size-1; x++ {
			c := m.dark[y*m.size+x]
			if c == m.dark[y*m.size+x+1] && c == m.dark[(y+1)*m.size+x] && c == m.dark[(y+1)*m.size+x+1] {
				result += penaltyN2
			}
		}
	}

	// 规则 4：黑白比例偏离 1:1 的程度
	dark := 0
	for _, d := range m.dark {
		if d {
			dark++
		}
	}
	total := m.size * m.size
	k := (abs(dark*20-total*10)+total-1)/total - 1
	result += k * penaltyN4
	return result
}

// addRun 把一段游程记进历史（最近的在 [0]）。
// 开头的浅色游程要额外加上整幅宽度：规则 3 的图形允许贴着边界。
func addRun(length int, history *[7]int, size int) {
	if history[0] == 0 {
		length += size
	}
	for i := len(history) - 1; i >= 1; i-- {
		history[i] = history[i-1]
	}
	history[0] = length
}

// countFinderPatterns 数历史里出现了几次 1:1:3:1:1（前后各要 4 倍宽度的浅色）。
func countFinderPatterns(history *[7]int) int {
	n := history[1]
	core := n > 0 && history[2] == n && history[3] == n*3 && history[4] == n && history[5] == n
	count := 0
	if core && history[0] >= n*4 && history[6] >= n {
		count++
	}
	if core && history[6] >= n*4 && history[0] >= n {
		count++
	}
	return count
}

// terminateRun 收尾一段游程，并返回这一行/列末尾该补的规则 3 罚分次数。
func terminateRun(runColor bool, runLen int, history *[7]int, size int) int {
	if runColor {
		addRun(runLen, history, size)
		runLen = 0
	}
	runLen += size
	addRun(runLen, history, size)
	return countFinderPatterns(history)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
