// This isolated CI fixture proves signing credentials without invoking the product runtime.
// 此独立 CI 测试程序验证签名凭据，不调用产品运行时。
package main

import "fmt"

// main prints a fixed marker; it accepts no input and accesses no external resources.
// main 输出固定标记，不接收输入，也不访问外部资源。
func main() {
	fmt.Println("OpenVulcan signing smoke test")
}
