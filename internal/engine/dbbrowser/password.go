package dbbrowser

import "fmt"

// validatePassword 是渲染面的共享安全闸（dbtemplate.validatePassword 同源
// 口径）：密码只准 [0-9A-Za-z]。动机是四方言的并集约束——pgpass 行格式
// 冒号不可转义、REDIS_HOSTS 冒号逐字段拆、mongodb URL userinfo 特殊字符、
// adminer JSON 值域；平台铸造公式 = hex 48（crypto/rand），用户面无覆写通道
// （PutSecret 拒 database: 保留前缀），本闸是纵深防御而非受理位。
func validatePassword(password string) error {
	if password == "" {
		return fmt.Errorf("dbbrowser: password must not be empty")
	}
	for i := 0; i < len(password); i++ {
		c := password[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			continue
		}
		return fmt.Errorf("dbbrowser: password carries characters outside the render-safe set [0-9A-Za-z]")
	}
	return nil
}
