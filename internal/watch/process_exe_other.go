//go:build !windows

package watch

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

func (*systemProcs) Exe(ctx context.Context, pid int32) (string, error) {
	p, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return "", err
	}
	return p.ExeWithContext(ctx)
}
