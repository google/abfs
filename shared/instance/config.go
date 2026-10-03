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

package instance

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"reflect"
	"strings"

	"github.com/dustin/go-humanize"
	"gopkg.in/yaml.v3"

	"github.com/google/abfs/shared/entities"
)

type configer interface {
	Encode(w io.Writer) error
	Decode(r io.Reader) error
	Validate() error
}

func (c *Config) Encode(w io.Writer) error {
	// The yaml library returns `{}` for zero-value struct
	if reflect.DeepEqual(c, &Config{}) {
		return nil
	}
	return yaml.NewEncoder(w).Encode(c)
}

func (c *Config) Decode(r io.Reader) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	err := dec.Decode(c)
	if errors.Is(err, io.EOF) {
		// The yaml library expects to have at least one component
		err = nil
	}
	return err
}

func (c *Config) Validate() error {
	if c.FSType != "" {
		if err := c.FSType.Set(string(c.FSType)); err != nil {
			return err
		}
	}
	if c.AuthType != "" {
		if err := c.AuthType.Set(string(c.AuthType)); err != nil {
			return err
		}
	}
	c.ManifestServer = MaybeRemoveScheme(c.ManifestServer)

	if c.RBEAsCAS {
		if c.RBEInstance == "" {
			return errors.New("RBE instance must be provided if RBE is enabled as CAS. Use the --rbe-instance flag or the 'rbe-instance' field in the config file")
		}
		if c.RBERemote == "" {
			return errors.New("RBE remote address must be provided if RBE is enabled as CAS. Use the --rbe-remote flag or the 'rbe-remote' field in the config file")
		}
	}

	return nil
}

func (c *Config) NonZeroToOverride() *ConfigOptional {
	o := &ConfigOptional{}
	vC := reflect.ValueOf(c).Elem()
	tC := vC.Type()
	vO := reflect.ValueOf(o).Elem()

	for i := 0; i < vC.NumField(); i++ {
		fieldC := vC.Field(i)
		fieldName := tC.Field(i).Name
		fieldO := vO.FieldByName(fieldName)

		if !fieldO.IsValid() || !fieldO.CanSet() {
			continue
		}

		isZero := false
		switch fieldC.Kind() {
		case reflect.Slice:
			isZero = fieldC.Len() == 0
		default:
			isZero = fieldC.IsZero()
		}

		if !isZero {
			ptr := reflect.New(fieldC.Type())
			ptr.Elem().Set(fieldC)
			fieldO.Set(ptr)
		}
	}

	return o
}

func (c *ConfigOptional) Encode(w io.Writer) error {
	// The yaml library returns `{}` for zero-value struct
	if reflect.DeepEqual(c, &ConfigOptional{}) {
		return nil
	}
	return yaml.NewEncoder(w).Encode(c)
}

func (c *ConfigOptional) EncodeHuman(w io.Writer) error {
	if reflect.DeepEqual(c, &ConfigOptional{}) {
		return nil
	}

	clone := c.Clone()

	type humanConfig struct {
		*ConfigOptional  `yaml:",inline"`
		EvictThreshold   *string `yaml:"evict-threshold-human,omitempty"`
		OfflineThreshold *string `yaml:"offline-threshold-human,omitempty"`
	}

	hc := humanConfig{
		ConfigOptional: clone,
	}

	if clone.EvictThreshold != nil {
		s := humanize.Bytes(*clone.EvictThreshold)
		hc.EvictThreshold = &s
		clone.EvictThreshold = nil
	}
	if clone.OfflineThreshold != nil {
		s := humanize.Bytes(*clone.OfflineThreshold)
		hc.OfflineThreshold = &s
		clone.OfflineThreshold = nil
	}

	return yaml.NewEncoder(w).Encode(hc)
}

func (c *ConfigOptional) Decode(r io.Reader) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	err := dec.Decode(c)
	if errors.Is(err, io.EOF) {
		// The yaml library expects to have at least one component
		err = nil
	}
	return err
}

func (c *ConfigOptional) Validate() error {
	if c.FSType != nil && *c.FSType != "" {
		if err := c.FSType.Set(string(*c.FSType)); err != nil {
			return err
		}
	}
	if c.AuthType != nil && *c.AuthType != "" {
		if err := c.AuthType.Set(string(*c.AuthType)); err != nil {
			return err
		}
	}
	if c.ManifestServer != nil {
		s := MaybeRemoveScheme(*c.ManifestServer)
		c.ManifestServer = &s
	}
	return nil
}

func (c *ConfigOptional) Clone() *ConfigOptional {
	c2 := new(ConfigOptional)
	c2.Apply(c)
	return c2
}

func (c *ConfigOptional) Apply(o *ConfigOptional) {
	if c == nil || o == nil {
		return
	}

	vC := reflect.ValueOf(c).Elem()
	vO := reflect.ValueOf(o).Elem()
	tO := vO.Type()

	for i := 0; i < vO.NumField(); i++ {
		fieldO := vO.Field(i)
		if fieldO.Kind() != reflect.Pointer {
			continue
		}
		if fieldO.IsNil() {
			continue
		}

		fieldName := tO.Field(i).Name
		fieldC := vC.FieldByName(fieldName)
		if fieldC.IsValid() && fieldC.CanSet() {
			fieldC.Set(fieldO)
		}
	}
}

func (c *ConfigOptional) ToConfig() *Config {
	config := &Config{}
	config.Apply(c)
	return config
}

func (c *ConfigOptional) RemoveUnchanged(config *Config) {
	if c == nil || config == nil {
		return
	}

	vC := reflect.ValueOf(c).Elem()
	vConfig := reflect.ValueOf(config).Elem()
	tC := vC.Type()

	for i := 0; i < vC.NumField(); i++ {
		fieldC := vC.Field(i)
		if fieldC.Kind() != reflect.Pointer {
			continue
		}
		if fieldC.IsNil() {
			continue
		}

		fieldName := tC.Field(i).Name
		fieldConfig := vConfig.FieldByName(fieldName)
		if fieldConfig.IsValid() && reflect.DeepEqual(fieldC.Elem().Interface(), fieldConfig.Interface()) {
			fieldC.Set(reflect.Zero(fieldC.Type()))
		}
	}
}

func (c *Config) Apply(o *ConfigOptional) {
	if c == nil || o == nil {
		return
	}

	vC := reflect.ValueOf(c).Elem()
	vO := reflect.ValueOf(o).Elem()
	tO := vO.Type()

	for i := 0; i < vO.NumField(); i++ {
		fieldO := vO.Field(i)
		if fieldO.Kind() != reflect.Pointer {
			continue
		}
		if fieldO.IsNil() {
			continue
		}

		fieldName := tO.Field(i).Name
		fieldC := vC.FieldByName(fieldName)
		if fieldC.IsValid() && fieldC.CanSet() {
			fieldC.Set(fieldO.Elem())
		}
	}
}

// Update the config with the given config (c2). It will ignore the c2 fields which have zero values.
func (c *Config) Update(c2 *Config) {
	// We achieve the update by concatenating the json representation of the config and let the later values win.
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(c); err != nil {
		panic(err)
	}
	if err := json.NewEncoder(buf).Encode(c2); err != nil {
		panic(err)
	}

	// Concatenated json is in the form of `{...}\n{...}`. We need to replace the middle part `}\n{`
	// with a comma to make it a valid single json document. The comma is not needed if any of the objects
	// are empty (like `{}`).
	content := bytes.Replace(buf.Bytes(), []byte("{}\n{"), []byte{'{'}, 1)
	content = bytes.Replace(content, []byte("}\n{}"), []byte{'}'}, 1)
	content = bytes.Replace(content, []byte("}\n{"), []byte{','}, 1)
	if err := json.Unmarshal(content, c); err != nil {
		panic(err)
	}
}

func (c *ConfigOptional) ApplyKeyValues(keyValues []string) error {
	fields := make(map[string]reflect.StructField, len(keyValues))
	t := reflect.TypeFor[Config]()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		yamlTag := field.Tag.Get("yaml")
		tags := strings.Split(yamlTag, ",")
		if len(tags) > 0 {
			fields[tags[0]] = field
		}
	}

	kvMap := make(map[string][]string, len(keyValues))
	for _, kv := range keyValues {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid key-value pair: %s", kv)
		}
		key, value := parts[0], parts[1]
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("invalid config key: %q", key)
		}
		kvMap[key] = append(kvMap[key], strings.TrimSpace(value))
	}

	yamlBuf := new(bytes.Buffer)
	for key, values := range kvMap {
		fmt.Fprintf(yamlBuf, "%s: ", key)
		field := fields[key]
		if field.Type.Kind() != reflect.Slice {
			fmt.Fprintf(yamlBuf, "%s\n", values[len(values)-1])
			continue
		}
		if len(values) == 1 && values[0] == "" {
			fmt.Fprintf(yamlBuf, "[]\n")
			continue
		}

		for i, item := range values {
			if field.Type.Elem().Kind() == reflect.String {
				item = fmt.Sprintf("%q", item)
			}
			values[i] = item
		}
		fmt.Fprintf(yamlBuf, "[%s]\n", strings.Join(values, ","))
	}

	if err := yaml.Unmarshal(yamlBuf.Bytes(), c); err != nil {
		return fmt.Errorf("failed to apply key-value config: %v", err)
	}

	return nil
}

func (c *ConfigOptional) Checksum() string {
	if c == nil {
		return ""
	}
	h := sha1.New()
	if err := c.Encode(h); err != nil {
		panic(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Config) Clone() *Config {
	c2 := new(Config)
	c2.Update(c)
	return c2
}

func (c *Config) EffectiveRemoteServers() []string {
	if c.LocalOnly {
		return nil
	}
	if len(c.TunnelPorts) == 0 {
		return c.RemoteServers
	}
	servers := make([]string, len(c.RemoteServers))
	for i, server := range c.RemoteServers {
		if i >= len(c.TunnelPorts) || c.TunnelPorts[i] == 0 {
			servers[i] = server
		} else {
			servers[i] = fmt.Sprintf("localhost:%d", c.TunnelPorts[i])
		}
	}
	return servers
}

func (c *Config) GetAuthInfo() AuthInfo {
	return CreateAuthInfo(c.AuthType, c.AuthAudience, c.RootCAFile, c.DisableTLS)
}

func ReadConfig[T configer](fileName string) (c T, retErr error) {
	var config T
	v := reflect.ValueOf(&config).Elem()
	if v.Kind() == reflect.Pointer && v.IsNil() {
		v.Set(reflect.New(v.Type().Elem()).Convert(v.Type()))
	}
	file, err := os.Open(fileName)
	if err != nil {
		return config, err
	}
	defer entities.CloseErrCheck(file, &retErr)

	if err := config.Decode(file); err != nil {
		return config, err
	}

	if err := config.Validate(); err != nil {
		return config, err
	}

	return config, nil
}

func WriteConfig[T configer](fileName string, config T, isAutoGen bool) error {
	if err := os.MkdirAll(path.Dir(fileName), os.FileMode(0755)); err != nil {
		return err
	}

	perm := 0600
	if isAutoGen {
		perm = 0400
	}
	file, err := os.OpenFile(fileName+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(perm))
	if err != nil {
		return err
	}
	defer func() {
		if file != nil {
			if err := file.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to close file: %v\n", err)
			}
		}
	}()
	if isAutoGen {
		if _, err := fmt.Fprintf(file, "# This file is auto-generated. Please DO NOT edit.\n\n"); err != nil {
			return err
		}
	}
	if err := config.Encode(file); err != nil {
		return err
	}
	err = file.Close()
	file = nil
	if err != nil {
		return err
	}

	return os.Rename(fileName+".tmp", fileName)
}
