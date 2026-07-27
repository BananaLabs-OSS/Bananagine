package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/bananalabs-oss/bananagine/templatecatalog"

	"bananagine-cell/compositionclient"
)

func callTemplateCatalog[T any](operation string, payload any) (templatecatalog.Result[T], error) {
	return compositionclient.Dispatch[templatecatalog.Result[T]](
		pulpRegistryCaller{},
		registryLuaTarget,
		operation,
		payload,
	)
}

func synchronizeTemplateCatalog(templates map[string]Template) error {
	names := make([]string, 0, len(templates))
	for name := range templates {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]templatecatalog.Entry, 0, len(names))
	for _, name := range names {
		template := templates[name]
		configJSON, err := json.Marshal(template.Config)
		if err != nil {
			return fmt.Errorf("encode template %q config: %w", name, err)
		}
		runtimeJSON, err := json.Marshal(template)
		if err != nil {
			return fmt.Errorf("encode template %q runtime: %w", name, err)
		}
		entries = append(entries, templatecatalog.Entry{
			Name:        template.Name,
			Game:        template.Game,
			Label:       template.Label,
			Engine:      template.Engine,
			CPULimit:    template.Container.CPULimit,
			MemoryLimit: template.Container.MemoryLimit,
			ConfigJSON:  configJSON,
			RuntimeJSON: runtimeJSON,
		})
	}
	fingerprint, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode template catalog: %w", err)
	}
	digest := sha256.Sum256(fingerprint)
	result, err := callTemplateCatalog[templatecatalog.Catalog](
		templatecatalog.FnReplace,
		templatecatalog.ReplaceRequest{
			RequestID: "template-catalog/" + hex.EncodeToString(digest[:]),
			Entries:   entries,
		},
	)
	if err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("template catalog replace: %s", result.Error)
	}
	return nil
}
