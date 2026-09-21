// Package export 承载导出物的落盘与容器编码：原子发布文件、写出 EPUB 容器。
// 它只认纯数据与 io.Writer，不认作品模型。
package export

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

// Publish 把 write 的输出原子发布到 path：先写同目录临时文件并 Sync，再一次性接入目标。
// overwrite=false 用硬链接独占创建，目标已存在时错误满足 errors.Is(err, os.ErrExist)；
// overwrite=true 用 Rename 替换。任何失败路径都不留下临时文件或半成品。
func Publish(ctx context.Context, path string, overwrite bool, write func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".ainovel-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := write(file); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if overwrite {
		return os.Rename(file.Name(), path)
	}
	return os.Link(file.Name(), path)
}
