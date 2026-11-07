package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/shichao-wang/cpa/internal/codexoauth"
	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/server"
	"github.com/shichao-wang/cpa/internal/store"
	"github.com/spf13/cobra"
)

// NewAppCommand 装配实际 binary 的连接、profile 和服务管理功能。
func NewAppCommand() *cobra.Command {
	var root *cobra.Command
	openStore := func() (*store.Store, error) { return store.New("") }
	root = NewCommandWithOptions(Options{
		Login: codexoauth.NewLogin().Login,
		OnChange: func() error {
			s, err := openStore()
			if err != nil {
				return err
			}
			return server.WaitForRevision(root.Context(), s)
		},
	})
	root.AddCommand(serverCommand(openStore))
	return root
}
func serverCommand(openStore storeFactory) *cobra.Command {
	parent := &cobra.Command{Use: "server", Short: "启动、查询和停止本地网关", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	var foreground bool
	var listen string
	start := &cobra.Command{Use: "start", Short: "启动网关（默认后台运行）", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("listen") {
			host, port, err := net.SplitHostPort(listen)
			if err != nil {
				return fmt.Errorf("监听地址必须是 host:port")
			}
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return fmt.Errorf("只允许 loopback 监听地址")
			}
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("监听端口必须在 1–65535")
			}
			info, err := server.Status(cmd.Context(), s)
			if err != nil {
				return err
			}
			if info.State != "stopped" {
				return fmt.Errorf("修改监听地址前必须先停止网关")
			}
			if err = s.Update(func(state *config.State) error { state.Listen = net.JoinHostPort(host, port); return nil }); err != nil {
				return err
			}
		}
		if foreground {
			return server.Foreground(cmd.Context(), s)
		}
		if err = server.Start(cmd.Context(), s); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "网关已启动")
		return err
	}}
	start.Flags().BoolVar(&foreground, "foreground", false, "以前台方式运行")
	start.Flags().StringVar(&listen, "listen", "", "loopback 监听地址，例如 127.0.0.1:8317")
	parent.AddCommand(start)
	parent.AddCommand(&cobra.Command{Use: "status", Short: "查询实际进程和就绪状态", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		info, err := server.Status(cmd.Context(), s)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
	}})
	parent.AddCommand(&cobra.Command{Use: "stop", Short: "停止本实例，不强制杀死其他进程", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		if err = server.Stop(cmd.Context(), s); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "网关已停止")
		return err
	}})
	return parent
}

// Execute 使用调用者的 context，避免测试或取消操作留下隐式后台工作。
func Execute(ctx context.Context, args []string) error {
	root := NewAppCommand()
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}
