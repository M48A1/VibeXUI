package server

import (
	"fmt"
	"net/http"
	"time"
	"vibexui/internal/kernel"
	"vibexui/internal/model"
)

func (s *Server) kernelVersions(w http.ResponseWriter, r *http.Request) {
	releases, err := s.kernels.List(r.Context())
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	respond(w, 200, map[string]any{"versions": releases})
}
func kernelReady(v *model.Server) error {
	if v == nil {
		return errNotFound
	}
	if !v.Kernel.Supported || kernel.AssetName(v.Kernel.Arch) == "" {
		return fmt.Errorf("请先升级该服务器的 Agent，内核管理支持 Linux amd64/arm64")
	}
	if v.LastSeen.IsZero() || time.Since(v.LastSeen) > 35*time.Second {
		return fmt.Errorf("服务器离线，请等待 Agent 在线后再切换")
	}
	if v.KernelTask != nil && (v.KernelTask.ID != v.Kernel.ID || !model.KernelComplete(v.Kernel.State)) {
		return fmt.Errorf("已有内核任务正在执行，请等待完成")
	}
	return nil
}
func (s *Server) changeKernel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action  string `json:"action"`
		Version string `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Action != "install" && in.Action != "rollback" {
		fail(w, 400, "不支持的内核操作")
		return
	}
	if in.Action == "install" && !kernel.ValidVersion(in.Version) {
		fail(w, 400, "版本格式无效")
		return
	}
	st, ok := s.view(w)
	if !ok {
		return
	}
	v := serverAt(&st, r.PathValue("id"))
	if err := kernelReady(v); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if in.Action == "install" {
		if err := s.kernels.Check(r.Context(), in.Version, v.Kernel.Arch); err != nil {
			fail(w, 502, err.Error())
			return
		}
	}
	var job model.KernelTask
	err := s.store.Update(func(st *model.State) error {
		v := serverAt(st, r.PathValue("id"))
		if err := kernelReady(v); err != nil {
			return err
		}
		if in.Action == "rollback" && v.Kernel.PreviousVersion == "" {
			return fmt.Errorf("没有可回滚的上一内核")
		}
		job = model.KernelTask{ID: model.ID(), Action: in.Action, Version: in.Version, CreatedAt: time.Now()}
		v.KernelTask = &job
		return nil
	})
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, 202, job)
}
func validKernelReport(r model.KernelReport) bool {
	if len(r.ID) > 64 || len(r.Error) > 1000 || len(r.Target) > 200 || len(r.PreviousVersion) > 200 || len(r.Arch) > 20 {
		return false
	}
	switch r.State {
	case "", "downloading", "switching", "succeeded", "failed":
		return true
	}
	return false
}
