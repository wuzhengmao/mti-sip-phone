package ui

// 窗口位置记忆的平台无关纯逻辑（REQ-UI-040 / REQ-VIDEO-015）。
// 平台相关部分（读光标、移动窗口、屏幕边界）在 topmost_windows.go /
// topmost_linux.go / topmost_other.go 里，三份同签名。这里只留可单测的部分。

// 保存的位置至少要留出这么多可见像素才算"还能用"（物理像素）。
// 不要求整个窗口在屏内：用户把窗口拖到屏幕边缘、露出一部分是常见操作，
// 只要能抓到一小块就够了；全出屏（拔掉副屏、改分辨率）才判为越界。
const (
	windowMinVisibleW = 64
	windowMinVisibleH = 24
)

// windowPosUsable 报告保存的窗口位置 (x,y,w,h) 是否还落在屏幕矩形
// (sx,sy,sw,sh) 内并且可见面积达标（物理像素；多显示器时原点可为负）。
//
// 尺寸未知（<=0，例如窗口还没显示）时退化为"左上角落在屏内"。
func windowPosUsable(x, y, w, h, sx, sy, sw, sh int) bool {
	if sw <= 0 || sh <= 0 {
		return true // 拿不到屏幕信息（非桌面平台）时不拦
	}
	if w <= 0 || h <= 0 {
		return x >= sx && x < sx+sw && y >= sy && y < sy+sh
	}
	// 两个矩形的交集
	ix := min(x+w, sx+sw) - max(x, sx)
	iy := min(y+h, sy+sh) - max(y, sy)
	return ix >= windowMinVisibleW && iy >= windowMinVisibleH
}

// dragTarget 按**锚点法**算拖动中的窗口目标位置：开始拖动时记下窗口左上角
// 与屏幕绝对光标，之后每次都从锚点重算，不做增量累加。
//
// 为什么不直接累加每帧位移：拖动时窗口跟着光标走，Fyne 的 DragEvent 坐标是
// **窗口相对**的——窗口一移动，相对坐标立刻回到起点，增量会抖甚至归零；
// 屏幕绝对光标不受窗口移动影响，锚点法既无自反馈也无累计漂移。
func dragTarget(winX, winY, cursorX, cursorY, startCursorX, startCursorY int) (int, int) {
	return winX + cursorX - startCursorX, winY + cursorY - startCursorY
}
