package cmd

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

// 这一组测试钉住一个很容易被"优化"掉的行为：
// **端口绑不上时必须报错，绝不能自己换一个端口偷偷跑起来。**
//
// 因为容器场景里端口映射是写死的（`-p 8080:8080`），程序一旦自己
// 换到 8081，映射关系就断了 —— 用户看到的是"容器在跑但网页打不开"，
// 完全无从排查。这个坑真实发生过，所以用测试锁死。

// TestListenOnConfiguredPort_Success 正常情况：端口空闲，能绑上。
func TestListenOnConfiguredPort_Success(t *testing.T) {
	// 先找一个当前空闲的端口
	probe, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("拿不到空闲端口: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	ln, err := listenOnConfiguredPort(port)
	if err != nil {
		t.Fatalf("空闲端口应该能绑上，却报错: %v", err)
	}
	defer ln.Close()

	if got := ln.Addr().(*net.TCPAddr).Port; got != port {
		t.Fatalf("绑定的端口变了：想要 %d，实际 %d", port, got)
	}
}

// TestListenOnConfiguredPort_Occupied 核心用例：
// 端口被占用时**必须返回错误**，而不是换一个端口。
func TestListenOnConfiguredPort_Occupied(t *testing.T) {
	// 先占住一个端口
	blocker, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port

	ln, err := listenOnConfiguredPort(port)

	// 1) 必须报错 —— 这是本测试的重点
	if err == nil {
		ln.Close()
		t.Fatalf("端口 %d 已被占用，却绑定成功了 —— "+
			"说明又回到了「静默换端口」的老路，容器里会表现为网页打不开", port)
	}

	// 2) 报错信息里要带上端口号，方便用户直接定位
	if !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Errorf("错误信息里应包含端口号 %d，便于排查，实际是: %v", port, err)
	}

	// 3) 错误信息要给出可操作的建议，而不是干巴巴一句 address already in use
	if !strings.Contains(err.Error(), "占用") {
		t.Errorf("错误信息应说明可能原因，实际是: %v", err)
	}
}

// TestListenOnConfiguredPort_NoSilentFallback 反向断言：
// 即使 8080 被占，也**不能**跑到 8081 上去。
//
// 这条测试比上面更严格：它直接检查"有没有别的端口被这个函数绑走"。
func TestListenOnConfiguredPort_NoSilentFallback(t *testing.T) {
	blocker, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port

	ln, err := listenOnConfiguredPort(port)
	if err == nil {
		got := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		t.Fatalf("期望失败，实际绑到了 %d —— 这就是「静默换端口」", got)
	}

	// 顺便确认下一个端口没有被它偷偷绑走
	next, err2 := net.Listen("tcp", ":"+strconv.Itoa(port+1))
	if err2 != nil {
		t.Fatalf("端口 %d 竟然被占用了 —— 说明函数内部尝试了下一个端口", port+1)
	}
	next.Close()
}

