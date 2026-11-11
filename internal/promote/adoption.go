package promote

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/zclconf/go-cty/cty"
	"pingone-promote/internal/overlay"
	"pingone-promote/internal/resourcecatalog"
)

var importSuffixRE = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+){0,3}$`)

func (m *merger) checkImportChoices() error {
	envs := map[string]bool{}
	for _, env := range m.envs() {
		envs[env] = true
	}
	prefix := "module." + m.l.ModuleName + "."
	for env, ids := range m.req.ImportIDs {
		if !envs[env] {
			return errors.New("promote: adoption environment is outside the rollout")
		}
		for full, id := range ids {
			a := strings.TrimPrefix(full, prefix)
			_, existing := m.head.defs[a]
			if full == a || (!existing && !m.inExport(a)) || !importable(a) || (len(id) > 2048 || !importSuffixRE.MatchString(id)) || len(scanValue("import_id", cty.StringVal(id))) > 0 {
				return errors.New("promote: adoption requires a managed address and a non-secret import ID suffix")
			}
			if resourcecatalog.EnvironmentOnly(strings.SplitN(a, ".", 2)[0]) && id != "environment" {
				return errors.New("promote: singleton adoption uses the environment marker")
			}
		}
	}
	return nil
}

func (m *merger) validateAdoption() error {
	for _, env := range m.envs() {
		p, err := m.l.Overlay(env)
		if err != nil {
			return err
		}
		src, ok, err := m.view.Read(p)
		if err != nil {
			return err
		}
		ids := map[string]cty.Value{}
		if ok {
			o, err := overlay.ParseOverlay(src, p)
			if err != nil {
				continue
			}
			v, ok := o.Values["import_ids"]
			if ok && !v.IsNull() && v.CanIterateElements() {
				for it := v.ElementIterator(); it.Next(); {
					k, v := it.Element()
					ids[k.AsString()] = v
				}
			}
		}
		for a, adopt := range m.req.Export.AdoptOnly {
			if !adopt || !m.written[a] {
				continue
			}
			full := m.l.ModuleAddress(a)
			if id, ok := ids[full]; ok && id.Type() == cty.String && !id.IsNull() && id.AsString() != "" {
				continue
			}
			m.res.Blockers = append(m.res.Blockers, Blocker{Env: env, Code: BlockMissingImport, Address: full, Message: fmt.Sprintf("%s must set import_ids for %s to its existing ID; built-in objects must be adopted", p, full)})
		}
	}
	return nil
}
