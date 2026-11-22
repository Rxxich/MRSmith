package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"mrsmith/internal/apperr"
	"mrsmith/internal/rules"
)

type convertSummary struct {
	Input struct {
		Format       rules.InputFormat `json:"format"`
		Behavior     rules.Behavior    `json:"behavior"`
		Lines        int               `json:"lines"`
		InvalidCount int               `json:"invalidCount"`
		Invalid      []string          `json:"invalid"`
		Size         int               `json:"size"`
		Name         string            `json:"name"`
	} `json:"input"`
	Result struct {
		Behavior  rules.Behavior  `json:"behavior"`
		Count     int             `json:"count"`
		Domains   int             `json:"domains"`
		CIDRs     int             `json:"cidrs"`
		Classical int             `json:"classical"`
		Skipped   int             `json:"skipped"`
		Dropped   map[string]int  `json:"dropped,omitempty"`
		Optimized int             `json:"optimized"`
		Sample    []string        `json:"sample"`
		Supported map[string]bool `json:"supported"`
		// MRS describes the part of a mixed (classical) list that goes into
		// an MRS file; MRSError says why none of it fits.
		MRS      *mrsPart           `json:"mrs,omitempty"`
		MRSError string             `json:"mrsError,omitempty"`
		Target   rules.OutputFormat `json:"target,omitempty"`
		Saved    map[string]string  `json:"saved,omitempty"`
	} `json:"result"`
}

type mrsPart struct {
	Behavior   rules.Behavior `json:"behavior"`
	Count      int            `json:"count"`
	Dropped    map[string]int `json:"dropped,omitempty"`
	Translated int            `json:"translated"`
	Sample     []string       `json:"sample"`
}

// handleConvert parses a file from an upload, a URL, pasted text or a bucket
// object, and either describes it, returns it converted, or saves the result.
func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) error {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Minute))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxFetchBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return badRequest("ожидается multipart/form-data")
	}
	f := map[string]string{}
	var data []byte
	name := ""
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if part.FileName() != "" {
			name = safeFileName(part.FileName())
			data, err = io.ReadAll(io.LimitReader(part, s.cfg.MaxFetchBytes+1))
			part.Close()
			if err != nil {
				return err
			}
			continue
		}
		limit := int64(4096)
		if part.FormName() == "text" {
			limit = s.cfg.MaxFetchBytes + 1
		}
		v, err := io.ReadAll(io.LimitReader(part, limit))
		part.Close()
		if err != nil {
			return err
		}
		if part.FormName() == "text" {
			data = v
			name = "pasted.txt"
		} else {
			f[part.FormName()] = strings.TrimSpace(string(v))
		}
	}

	switch f["source"] {
	case "url":
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		res, err := s.fetch.Get(ctx, f["url"], "", "")
		cancel()
		if err != nil {
			return apperr.New(http.StatusUnprocessableEntity, "не удалось скачать: %v", err)
		}
		data = res.Body
		name = path.Base(strings.SplitN(f["url"], "?", 2)[0])
	case "object":
		o, err := s.st.Object(f["bucket"], f["key"])
		if err != nil {
			return err
		}
		if data, err = s.st.ReadAll(o, s.cfg.MaxFetchBytes); err != nil {
			return err
		}
		name = o.Name()
	}
	if int64(len(data)) > s.cfg.MaxFetchBytes {
		return apperr.TooLarge(fmt.Sprintf("файл больше %d МБ", s.cfg.MaxFetchBytes>>20))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return badRequest("нет данных: выберите файл, ссылку или вставьте текст")
	}

	inFormat := rules.InputFormat(f["inputFormat"])
	if inFormat == "" {
		inFormat = rules.InputAuto
	}
	behavior := rules.Behavior(f["behavior"])
	if behavior == "" {
		behavior = rules.BehaviorAuto
	}
	if !inFormat.Valid() || !behavior.Valid() {
		return badRequest("неизвестный формат или behavior")
	}
	pr, err := rules.Parse(data, rules.ParseOptions{Format: inFormat, BareAsSuffix: f["bareAsSuffix"] == "true"})
	if err != nil {
		return apperr.New(http.StatusUnprocessableEntity, "%v", err)
	}
	set := pr.Set
	optimized := 0
	if f["optimize"] == "true" {
		optimized = set.Optimize()
	}
	opts := rules.RenderOptions{AggregateIP: f["aggregateIP"] == "true", NoResolve: f["noResolve"] == "true"}
	built := rules.Resolve(set, behavior, opts)
	// A mixed list still gets an MRS file: the domains or the IP networks,
	// whichever there are more of.
	var mrsBuilt *rules.Built
	var mrsErr error
	if built.Behavior == rules.BehaviorClassical {
		mrsBuilt, mrsErr = rules.ResolveMRS(set, rules.BehaviorAuto, opts)
	}

	var sum convertSummary
	sum.Input.Format, sum.Input.Behavior = pr.Format, pr.Behavior
	sum.Input.Lines, sum.Input.InvalidCount, sum.Input.Invalid = pr.Lines, pr.InvalidCount, pr.Invalid
	sum.Input.Size, sum.Input.Name = len(data), name
	if sum.Input.Invalid == nil {
		sum.Input.Invalid = []string{}
	}
	res := &sum.Result
	res.Behavior, res.Count, res.Skipped, res.Optimized = built.Behavior, built.Count(), built.Skipped, optimized
	res.Dropped = built.Dropped
	if mrsBuilt != nil {
		ml := mrsBuilt.Lines()
		res.MRS = &mrsPart{Behavior: mrsBuilt.Behavior, Count: mrsBuilt.Count(), Dropped: mrsBuilt.Dropped,
			Translated: mrsBuilt.Translated, Sample: ml[:min(len(ml), 50)]}
	} else if mrsErr != nil {
		res.MRSError = mrsErr.Error()
	}
	res.Domains, res.CIDRs, res.Classical = len(built.Domains), len(built.Prefixes), len(built.Classical)
	lines := built.Lines()
	res.Sample = lines[:min(len(lines), 300)]
	res.Supported = map[string]bool{}
	for _, of := range rules.AllOutputs {
		res.Supported[string(of)] = of.Supports(built.Behavior) && built.Count() > 0
	}
	if mrsBuilt != nil {
		res.Supported[string(rules.OutMRS)] = true
	}

	mode := f["mode"]
	if mode == "inspect" || mode == "" {
		writeJSON(w, http.StatusOK, sum)
		return nil
	}

	target := rules.OutputFormat(f["target"])
	if !target.Valid() {
		return badRequest("выберите формат результата")
	}
	if built.Count() == 0 {
		return badRequest("после обработки не осталось ни одного правила")
	}
	render := built
	if target == rules.OutMRS && built.Behavior == rules.BehaviorClassical {
		if mrsBuilt == nil {
			return apperr.New(http.StatusUnprocessableEntity, "%v", mrsErr)
		}
		render = mrsBuilt
	}
	out, err := render.Render(target)
	if err != nil {
		return apperr.New(http.StatusUnprocessableEntity, "%v", err)
	}
	base := strings.TrimSuffix(name, path.Ext(name))
	if base == "" || base == "." {
		base = "rules"
	}
	outName := base + "." + string(target)

	switch mode {
	case "download":
		h := w.Header()
		h.Set("Content-Type", target.ContentType())
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": outName}))
		h.Set("X-Rule-Count", fmt.Sprint(render.Count()))
		_, _ = w.Write(out)
		return nil
	case "save":
		key := f["saveKey"]
		if key == "" {
			key = outName
		}
		if err := s.st.Writable(f["saveBucket"]); err != nil {
			return err
		}
		obj, err := s.st.Put(f["saveBucket"], key, bytes.NewReader(out), "")
		if err != nil {
			return err
		}
		res.Target = target
		res.Saved = map[string]string{"bucket": obj.Bucket, "key": obj.Key}
		writeJSON(w, http.StatusOK, sum)
		return nil
	}
	return badRequest("неизвестное действие")
}
