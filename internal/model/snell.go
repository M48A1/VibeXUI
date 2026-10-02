package model

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
)

type SnellInbound struct {
	ID       string `json:"id"`
	ServerID string `json:"serverId"`
	Name     string `json:"name"`
	Listen   string `json:"listen"`
	Port     int    `json:"port"`
	PSK      string `json:"psk,omitempty"`
	Enabled  bool   `json:"enabled"`
	TFO      bool   `json:"tfo"`
	IPv6     bool   `json:"ipv6"`
	Revision int64  `json:"revision"`
}
type SnellStatus struct {
	Revision int64  `json:"revision"`
	Running  bool   `json:"running"`
	State    string `json:"state"`
	Error    string `json:"error"`
}

var snellID = regexp.MustCompile(`^[A-Za-z0-9_-]{16}$`)
var snellKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func ValidateSnell(n SnellInbound) error {
	if !snellID.MatchString(n.ID) || n.Revision < 1 {
		return fmt.Errorf("Snell 标识或版本无效")
	}
	if len(n.Name) == 0 || len(n.Name) > 100 {
		return fmt.Errorf("名称须为 1–100 字节")
	}
	if net.ParseIP(n.Listen) == nil {
		return fmt.Errorf("监听地址须为 IP")
	}
	if n.Port < 1 || n.Port > 65535 || n.Port == 10085 {
		return fmt.Errorf("端口无效或被统计服务保留")
	}
	if !snellKey.MatchString(n.PSK) {
		return fmt.Errorf("PSK 须为 16–128 位字母、数字、下划线或连字符")
	}
	return nil
}
func SnellConfig(n SnellInbound) ([]byte, error) {
	if err := ValidateSnell(n); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("[snell-server]\nlisten = %s\npsk = %s\nipv6 = %t\ntfo = %t\n", net.JoinHostPort(n.Listen, strconv.Itoa(n.Port)), n.PSK, n.IPv6, n.TFO)), nil
}
func CheckSnellPort(st State, sid string, port int) error {
	for _, n := range st.Snell {
		if n.ServerID == sid && n.Port == port {
			return fmt.Errorf("端口已被 Snell 入站占用")
		}
	}
	return nil
}
