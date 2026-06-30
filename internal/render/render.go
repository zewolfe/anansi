package render

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zewolfe/anansi/internal/config"
	"gopkg.in/yaml.v3"
)

const runtimeClassName string = "nvidia"

type Renderer struct {
	outputDir string
}

type isvcKey struct{ Runtime, Format, Caching, Model string }

var s3RegEx = regexp.MustCompile(`^s3://[^/]+/`)

func NewRenderer(outputDir string) *Renderer {
	return &Renderer{outputDir: outputDir}
}

func (r *Renderer) Render(cfg *config.BenchConfig) error {
	if err := os.MkdirAll(r.outputDir, 0755); err != nil {
		return fmt.Errorf("ERROR: Failed to create output directory: %w", err)
	}

	trials, err := config.ExpandMatrix(cfg)
	if err != nil {
		return fmt.Errorf("ERROR: Failed render when expanding matrix, %s", err)
	}

	seen := make(map[isvcKey]bool)
	for _, t := range trials {
		key := isvcKey{t.Runtime.Name, t.Format.Name, t.Caching.Name, t.Model.Name}
		if seen[key] {
			continue
		}
		seen[key] = true

		isvc := ISVC{
			APIVersion: "serving.kserve.io/v1beta1",
			Kind:       "InferenceService",
			Metadata: ISVCMetadata{
				Name:      ISVCName(t.Runtime.Name, t.Format.Name, t.Caching.Name, t.Model.Name),
				Namespace: cfg.Testbed.Namespace,
				Annotations: map[string]string{
					"autoscaling.knative.dev/minScale":                      "0",
					"autoscaling.knative.dev/maxScale":                      "1",
					"autoscaling.knative.dev/scaleToZeroPodRetentionPeriod": "30s",
					"autoscaling.knative.dev/window":                        "60s",
				},
				Labels: map[string]string{
					"anansi.runtime": t.Runtime.Name,
					"anansi.format":  t.Format.Name,
					"anansi.caching": t.Caching.Name,
					"anansi.model":   t.Model.Name,
				},
			},
			Spec: ISVCSpec{
				Predictor: PredictorSpec{
					ServiceAccountName: serviceAccountFor(t.Caching),
					RuntimeClassName:   runtimeClassName,
					MinReplicas:        0,
					MaxReplicas:        1,
					Model: ModelSpec{
						ModelFormat: ModelFormat{Name: modelFormatFor(t.Format)},
						Runtime:     runtimeName(t.Runtime),
						StorageUri:  StorageURI(t.Format.Name, t.Model.Name, t.Scenario.Name),
						//TODO:Support VLLM ENV VARIABLES
						Env: buildRuntimeEnv(t),
						Resources: &Resources{
							Limits: map[string]string{"nvidia.com/gpu": "1"},
						},
					},
				},
			},
		}

		name := isvc.Metadata.Name
		err = r.SaveRender(name, isvc)
		if err != nil {
			return fmt.Errorf("ERROR: Failed to save Inference Service manifest: %w", err)

		}
	}

	return nil
}

func buildRuntimeEnv(t config.TrialConfig) []EnvVar {
	env := []EnvVar{
		{Name: "ANANSI_LOADER", Value: t.Runtime.Loader},
		{Name: "ANANSI_LOADER_ARGS", Value: t.Runtime.LoaderArgs},
		{Name: "MODEL_PATH", Value: ModelPath(t.Format.Name, t.Model.Name, t.Scenario.Name)},
	}

	keys := make([]string, 0, len(t.Runtime.Env))
	for k := range t.Runtime.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, EnvVar{Name: k, Value: t.Runtime.Env[k]})
	}

	return env
}

func (r *Renderer) SaveRender(isvcKey string, isvc ISVC) error {
	path := filepath.Join(r.outputDir, isvcKey+".yaml")
	file, err := os.OpenFile(path, os.O_TRUNC|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("ERROR: Failed to open file for writing ISVC: %s", err)
	}

	defer file.Close()

	encoder := yaml.NewEncoder(file)
	defer encoder.Close()

	err = encoder.Encode(isvc)
	if err != nil {
		return fmt.Errorf("ERROR: Failed to encode ISVC to file: %s", err)
	}

	return nil
}

func serviceAccountFor(caching config.CachingDef) string {
	if caching.LocalModelCache {
		return "kserve-bench-pvc"
	}

	return "kserve-bench-s3"
}

func modelFormatFor(format config.FormatDef) string {
	return strings.Split(format.Name, "-")[0]
}

func runtimeName(runtime config.RuntimeDef) string {
	return strings.Split(runtime.Name, "-")[0]
}

func ISVCName(runtime, format, caching, model string) string {
	raw := fmt.Sprintf("%s-%s-%s-%s", runtime, format, caching, model)
	return sanitiseK8sName(raw)
}

func sanitiseK8sName(name string) string {
	name = strings.ReplaceAll(name, ".", "-")
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "_", "-")

	maxchar := 63
	if len(name) > maxchar {
		name = name[:maxchar]
	}

	return name
}

func isLMCScenario(scenario string) bool {
	return strings.HasPrefix(scenario, "s2-lmc") || strings.HasPrefix(scenario, "s3-lmc")
}

func isMultiFileFormat(format string) bool {
	return !strings.HasPrefix(format, "gguf-")
}

func StorageURI(format, model, scenario string) string {
	if isLMCScenario(scenario) {
		return "pvc://models-cache"
	}

	return fmt.Sprintf("s3://models/%s/%s", model, format)
}

func ModelPath(format, model, scenario string) string {
	if isLMCScenario(scenario) {
		return fmt.Sprintf("/mnt/models/%s/%s", model, format)
	}

	if isMultiFileFormat(format) {
		return "/mnt/models"
	}

	return fmt.Sprintf("/mnt/models/%s", format)
}
