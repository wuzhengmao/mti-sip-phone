package sip

// CANCEL 的 Record-Route 清理（真缺陷修复，与服务器配置无关）。
//
// CANCEL 必须按 **INVITE 的路径**发（RFC 3261 §9.1：目的地址、Call-ID、CSeq、
// Via branch 都与 INVITE 一致），**不能带对话的路由集**。但 sipgo 构造 CANCEL 时
// 走的是 dialog 的 buildReq，会把**响应里的 Record-Route** 当成 Route 头塞进去，
// 于是 CANCEL 被发到 Record-Route 指向的地址——那条腿未必连得上（真机：TLS 注册
// 时它是明文口），CANCEL 静默丢失、对端一直振铃。挂断振铃中的呼出时把**临时响应**
// （1xx）上的 Record-Route 摘掉，CANCEL 即回到 INVITE 的原始目的地；
// 最终响应（2xx）的不能摘——那要留给补发的 ACK/BYE。

// stripRecordRoutes 摘掉消息上**全部** Record-Route 头。
//
// ⚠️ 不能用单次 `RemoveHeader("Record-Route")`：sipgo 的实现（headers.go）是
// **大小写敏感、且只删第一条**，而真机的响应上可能有多条（OpenSIPS 双
// Record-Route），只删一条会留下另一条，CANCEL 于是被送到剩下的那条 Route 上。
func stripRecordRoutes(msg interface{ RemoveHeader(string) bool }) {
	for _, name := range []string{"Record-Route", "record-route"} {
		for msg.RemoveHeader(name) {
		}
	}
}
