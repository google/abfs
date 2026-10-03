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

package abfs

import (
	"strings"

	"github.com/google/abfs/manifest"
)

func overlaysMap(man *manifest.Manifest, overlays []string, views []string) (*manifest.Manifest, error) {
	var err error
	man, err = manifest.CloneManifest(man)
	if err != nil {
		return nil, err
	}
	names := make(map[string]struct{}, len(man.Project))
	for _, prj := range man.Project {
		names[prj.GetPath()] = struct{}{}
	}

	prefixed := make([]string, len(overlays))
	for i, o := range overlays {
		prefixed[i] = "overlays/" + o + "/"
	}
	overlayProjects(man, prefixed, views)
	updateOverlayLinks(man, prefixed)
	return man, nil
}

// This method filters out overlay projects not in the configured overlays input.
func overlayProjects(man *manifest.Manifest, overlays []string, views []string) {
	projectsMap := make(map[string]*manifest.Project, len(man.Project))
	ordered := []any{}
	includeProject := func(prj *manifest.Project, prjPath string) {
		prj.Path = prjPath
		ordered = append(ordered, prj)
		projectsMap[prjPath] = prj
	}

	handleProject := func(prj *manifest.Project) {
		if shouldExcludeProject(prj, []string{}) {
			return
		}
		p := prj.GetPath()
		if _, ok := projectsMap[p]; ok {
			return
		}
		if !strings.HasPrefix(p, "overlays/") {
			includeProject(prj, p)
			return
		}

		tokens := strings.Split(p, "/")
		if len(tokens) < 3 {
			return
		}

		// Include projects from the overlay
		for _, o := range overlays {
			if strings.HasPrefix(p, o) {
				prjPath := p[len(o):]
				includeProject(prj, prjPath)
				return
			}
		}

		// Include overlay projects if the src of the project is a prefix to a view mount.
		projOverlayPrefix := tokens[0] + "/" + tokens[1] + "/"
		for _, v := range views {
			if strings.HasPrefix(v, p) {
				prjPath := p[len(projOverlayPrefix):]
				includeProject(prj, prjPath)
				return
			}
		}
	}

	for _, ele := range man.Ordered {
		prj, ok := ele.(*manifest.Project)
		if !ok {
			// Any non-project element remains in ordered as is.
			ordered = append(ordered, ele)
			continue
		}

		handleProject(prj)
	}

	// Keep Project and OrderedXProject in sync with Ordered
	man.Ordered = ordered
	man.Project = nil
	man.OrderedXProject = nil
	for _, ele := range man.Ordered {
		switch ele := ele.(type) {
		case *manifest.Project:
			man.Project = append(man.Project, ele)
			man.OrderedXProject = append(man.OrderedXProject, ele)
		case *manifest.RemoveProject, *manifest.ExtendProject:
			man.OrderedXProject = append(man.OrderedXProject, ele)
		default:
			// Do nothing
		}
	}
}

func updateOverlayLinks(man *manifest.Manifest, overlays []string) {
	linksMap := make(map[string]*manifest.Project, len(man.Project))

	for _, prj := range man.Project {
		for _, link := range prj.Linkfile {
			if _, ok := linksMap[link.Dest]; ok {
				continue
			}
			if strings.HasPrefix(link.Dest, "overlays/") {
				for _, o := range overlays {
					if strings.HasPrefix(link.Dest, o) {
						linksMap[link.Dest[len(o):]] = prj
						break
					}
				}
			} else {
				linksMap[link.Dest] = prj
			}
		}
	}

	for _, prj := range man.Project {
		links := make([]*manifest.Linkfile, 0, len(prj.Linkfile))
		for _, lp := range prj.Linkfile {
			link := *lp
			if strings.HasPrefix(link.Dest, "overlays/") {
				for _, o := range overlays {
					if strings.HasPrefix(link.Dest, o) && linksMap[link.Dest[len(o):]] == prj {
						link.Dest = link.Dest[len(o):]
						links = append(links, &link)
						break
					}
				}
			} else {
				if linksMap[link.Dest] == prj {
					links = append(links, &link)
				}
			}
		}
		prj.Linkfile = links
	}
}
