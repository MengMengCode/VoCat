package i18n

// Keep feature-specific diagnostic strings together so additions to the proxy
// probe do not cause conflicts in the shared dictionary.
func init() {
	zhToEn["UDP ASSOCIATE 已建立，但实际 UDP 数据没有返回；检查节点 UDP 转发、路由和防火墙。"] = "UDP ASSOCIATE was established, but no UDP payload returned; check the node's UDP forwarding, routing, and firewall."
	zhToEn["TCP 握手、认证、UDP ASSOCIATE 与真实 UDP DNS 往返均通过。"] = "TCP handshake, authentication, UDP ASSOCIATE, and a real UDP DNS round trip all passed."
	zhToEn["代理已保存，SOCKS5 认证与真实 UDP 往返均通过。"] = "Proxy saved; SOCKS5 authentication and a real UDP round trip both passed."
	zhToEn["SOCKS5 认证与真实 UDP 往返探测通过。"] = "SOCKS5 authentication and a real UDP round-trip probe passed."
}
