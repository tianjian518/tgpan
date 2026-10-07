package retry

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

var internalErrors = []string{
	"Timedout",
	"No workers running",
	"RPC_CALL_FAIL",
	"RPC_MCGET_FAIL",
	"WORKER_BUSY_TOO_LONG_RETRY",
	"memory limit exit",
	"connection dead",
	"engine was closed",
	"STORAGE_CHOOSE_VOLUME_FAILED",
}

type retry struct {
	max    int
	errors []string
}

func isErrorMatch(err error) bool {
	if err == nil {
		return false
	}
	// 【为什么用 Contains 而不是 errors.Is】
	//
	// 原来写的是 errors.Is(err, errors.New(internalError))，这里有个致命问题：
	// errors.New 每次调用都返回**全新的实例**，而 errors.Is 比的是「错误链上
	// 有没有同一个实例」（指针相等），不是「文字是否相同」。
	//
	// 于是 errors.Is(netErr, errors.New("connection dead")) 恒为 false ——
	// 这个重试列表**从来没生效过**。网络抖一下本该重试的，实际一次就抛给用户，
	// 表现就是登录/扫描时偶发「connection dead」「Timedout」直接失败。
	//
	// 改用文字包含匹配：这些字符串本来就是 gotd/td 内部约定的错误文案，
	// 只要错误信息里出现就算命中。宁可多重试几次，也不要漏掉重试。
	msg := err.Error()
	for _, internalError := range internalErrors {
		if strings.Contains(msg, internalError) {
			return true
		}
	}
	// 兜底：万一 err 被包装过、Error() 里没带原文，也沿着错误链找一圈。
	for _, internalError := range internalErrors {
		if errors.Is(err, errors.New(internalError)) {
			return true
		}
	}
	return false
}

func (r retry) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		retries := 0

		for retries < r.max {
			if err := next.Invoke(ctx, input, output); err != nil {
				if tgerr.Is(err, r.errors...) || isErrorMatch(err) {
					retries++
					continue
				}
				return errors.Wrap(err, "retry middleware skip")
			}

			return nil
		}

		return fmt.Errorf("retry limit reached after %d attempts", r.max)
	}
}

func New(max int, errors ...string) telegram.Middleware {
	return retry{
		max:    max,
		errors: append(errors, internalErrors...),
	}
}
