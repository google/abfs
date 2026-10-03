// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package editor

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"
)

// VimValueOption represents a single value completion candidate for a YAML key in Vim.
type VimValueOption struct {
	Word string
	Menu string
}

// VimKeySpec represents a YAML key and its allowed value completion candidates in Vim.
type VimKeySpec struct {
	Key    string
	Menu   string
	Values []VimValueOption
}

type enumValuesProvider interface {
	EnumValues() [][2]string
}

// BuildStructVimSpecs extracts YAML tags, Go types, `desc` struct tags, and enum value
// options (via EnumValues() [][2]string) using reflection on struct T.
func BuildStructVimSpecs[T any]() []VimKeySpec {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	specs := make([]VimKeySpec, 0, t.NumField())

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		yamlTag := field.Tag.Get("yaml")
		if yamlTag == "" {
			continue
		}
		key := strings.Split(yamlTag, ",")[0]
		if key == "" || key == "-" {
			continue
		}

		var typeLabel string
		var values []VimValueOption

		ft := field.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		var enumOpts [][2]string
		if reflect.PointerTo(ft).Implements(reflect.TypeFor[enumValuesProvider]()) {
			enumOpts = reflect.New(ft).Interface().(enumValuesProvider).EnumValues()
		} else if ft.Implements(reflect.TypeFor[enumValuesProvider]()) {
			enumOpts = reflect.Zero(ft).Interface().(enumValuesProvider).EnumValues()
		}

		if len(enumOpts) > 0 {
			words := make([]string, len(enumOpts))
			for j, opt := range enumOpts {
				words[j] = opt[0]
				values = append(values, VimValueOption{Word: opt[0], Menu: opt[1]})
			}
			typeLabel = strings.Join(words, "|")
		} else {
			switch {
			case ft == reflect.TypeFor[time.Duration]():
				typeLabel = "duration"
			case ft.Kind() == reflect.Bool:
				typeLabel = "bool"
				values = []VimValueOption{
					{Word: "true", Menu: "[bool]"},
					{Word: "false", Menu: "[bool]"},
				}
			case ft.Kind() == reflect.Slice:
				typeLabel = "[]" + ft.Elem().String()
			default:
				typeLabel = ft.String()
			}
		}

		desc := field.Tag.Get("desc")
		menu := fmt.Sprintf("[%s]", typeLabel)
		if desc != "" {
			menu = fmt.Sprintf("[%s] %s", typeLabel, desc)
		}

		specs = append(specs, VimKeySpec{
			Key:    key,
			Menu:   menu,
			Values: values,
		})
	}
	return specs
}

func escapeVimSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// SetupVimSupport creates a temporary dictionary file and Vimscript file in dir
// to configure syntax highlighting, keyword characters, and key/value autocomplete.
func SetupVimSupport(dir string, filetype string, specs []VimKeySpec) (vimScriptPath string, cleanup func(), err error) {
	var dictPath string
	if len(specs) > 0 {
		dictFile, err := os.CreateTemp(dir, ".abfs-vim-*.dict")
		if err != nil {
			return "", nil, err
		}
		dictPath = dictFile.Name()

		var dictBuf bytes.Buffer
		seenWords := make(map[string]bool)
		for _, spec := range specs {
			if !seenWords[spec.Key] {
				dictBuf.WriteString(spec.Key + "\n")
				seenWords[spec.Key] = true
			}
			for _, v := range spec.Values {
				if !seenWords[v.Word] {
					dictBuf.WriteString(v.Word + "\n")
					seenWords[v.Word] = true
				}
			}
		}
		if _, err := dictFile.Write(dictBuf.Bytes()); err != nil {
			_ = dictFile.Close()
			_ = os.Remove(dictPath)
			return "", nil, err
		}
		if err := dictFile.Close(); err != nil {
			_ = os.Remove(dictPath)
			return "", nil, err
		}
	}

	scriptFile, err := os.CreateTemp(dir, ".abfs-vim-*.vim")
	if err != nil {
		if dictPath != "" {
			_ = os.Remove(dictPath)
		}
		return "", nil, err
	}
	scriptPath := scriptFile.Name()

	var scriptBuf bytes.Buffer
	scriptBuf.WriteString("syntax on\n")
	if filetype != "" {
		fmt.Fprintf(&scriptBuf, "setlocal filetype=%s\n", filetype)
	}
	scriptBuf.WriteString("setlocal iskeyword+=-,.\n")
	if dictPath != "" {
		fmt.Fprintf(&scriptBuf, "execute 'setlocal complete+=k' . fnameescape('%s')\n", escapeVimSingleQuote(dictPath))
	}

	if len(specs) > 0 {
		scriptBuf.WriteString("if has('eval')\n")
		scriptBuf.WriteString("  setlocal completeopt=menuone\n")
		scriptBuf.WriteString("  inoremap <buffer><expr> <Tab> pumvisible() ? \"\\<C-n>\" : \"\\<Tab>\"\n")
		scriptBuf.WriteString("  inoremap <buffer><expr> <S-Tab> pumvisible() ? \"\\<C-p>\" : \"\\<S-Tab>\"\n")
		scriptBuf.WriteString("  inoremap <buffer><expr> <CR> pumvisible() ? \"\\<C-y>\" : \"\\<CR>\"\n")
		scriptBuf.WriteString("  let s:abfs_keys = [\n")
		for _, spec := range specs {
			scriptBuf.WriteString("    \\ {'key': '" + escapeVimSingleQuote(spec.Key) + "', 'menu': '" + escapeVimSingleQuote(spec.Menu) + "', 'values': [")
			for j, v := range spec.Values {
				if j > 0 {
					scriptBuf.WriteString(", ")
				}
				scriptBuf.WriteString("{'word': '" + escapeVimSingleQuote(v.Word) + "', 'menu': '" + escapeVimSingleQuote(v.Menu) + "'}")
			}
			scriptBuf.WriteString("]},\n")
		}
		scriptBuf.WriteString("  \\ ]\n")

		scriptBuf.WriteString(`  function! AbfsConfigComplete(findstart, base) abort
    let line = getline('.')
    let col = col('.') - 1
    let prefix = col > 0 ? line[0 : col - 1] : ''
    let colon_idx = stridx(prefix, ':')
    if a:findstart
      if colon_idx >= 0
        let val_start = colon_idx + 1
        while val_start < col && line[val_start] =~# '\s'
          let val_start += 1
        endwhile
        return val_start
      else
        let key_start = 0
        while key_start < col && line[key_start] =~# '\s'
          let key_start += 1
        endwhile
        return key_start
      endif
    else
      let res = []
      if colon_idx >= 0
        let key_name = trim(prefix[0 : colon_idx - 1])
        for item in s:abfs_keys
          if item.key ==# key_name
            for val in item.values
              if stridx(val.word, a:base) == 0
                call add(res, val)
              endif
            endfor
          endif
        endfor
      else
        let rest = line[col :]
        let suffix = stridx(rest, ':') >= 0 ? '' : ': '
        for item in s:abfs_keys
          if stridx(item.key, a:base) == 0
            call add(res, {'word': item.key . suffix, 'abbr': item.key, 'menu': item.menu})
          endif
        endfor
      endif
      return res
    endif
  endfunction
  setlocal omnifunc=AbfsConfigComplete
  setlocal completefunc=AbfsConfigComplete
  try
    setlocal complete^=FAbfsConfigComplete
  catch
  endtry
  autocmd VimEnter * echohl ModeMsg | echo 'ABFS Editor: Press <C-n> or <C-x><C-o> for autocomplete' | echohl None
endif
`)
	}

	if _, err := scriptFile.Write(scriptBuf.Bytes()); err != nil {
		_ = scriptFile.Close()
		_ = os.Remove(scriptPath)
		if dictPath != "" {
			_ = os.Remove(dictPath)
		}
		return "", nil, err
	}
	if err := scriptFile.Close(); err != nil {
		_ = os.Remove(scriptPath)
		if dictPath != "" {
			_ = os.Remove(dictPath)
		}
		return "", nil, err
	}

	cleanup = func() {
		_ = os.Remove(scriptPath)
		if dictPath != "" {
			_ = os.Remove(dictPath)
		}
	}
	return scriptPath, cleanup, nil
}
