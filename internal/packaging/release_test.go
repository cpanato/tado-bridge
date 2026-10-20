package packaging_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"text/template"

	"go.yaml.in/yaml/v3"
)

func TestReleaseTagsAndFixtureIsolation(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Kos []struct {
			ID      string   `yaml:"id"`
			Tags    []string `yaml:"tags"`
			Disable string   `yaml:"disable"`
		} `yaml:"kos"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		IsSnapshot bool
		Tag        string
		Prerelease string
		want       []string
	}{
		{"snapshot", true, "v0.0.0", "", []string{"snapshot"}},
		{"stable", false, "v1.2.3", "", []string{"v1.2.3", "latest"}},
		{"prerelease", false, "v1.2.3-rc.1", "rc.1", []string{"v1.2.3-rc.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			render := func(source string) string {
				t.Helper()
				tmpl, err := template.New("release").Parse(source)
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				if err := tmpl.Execute(&output, tc); err != nil {
					t.Fatal(err)
				}
				return output.String()
			}
			found := 0
			for _, image := range config.Kos {
				switch image.ID {
				case "container":
					found++
					var tags []string
					for _, source := range image.Tags {
						if tag := render(source); tag != "" {
							tags = append(tags, tag)
						}
					}
					if !slices.Equal(tags, tc.want) {
						t.Fatalf("image tags %v, want %v", tags, tc.want)
					}
				case "fixture":
					found++
					if disabled := render(image.Disable); (disabled == "true") != !tc.IsSnapshot {
						t.Fatal("fixture must be disabled for every tagged release")
					}
				}
			}
			if found != 2 {
				t.Fatal("missing application or fixture image configuration")
			}
		})
	}
}
