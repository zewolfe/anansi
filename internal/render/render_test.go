package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zewolfe/anansi/internal/config"
	"gopkg.in/yaml.v3"
)

// Canonical fixtures. The matrix expands the cartesian product of these;
// tests below enumerate the cases that exercise scenario-aware branching.
var (
	allModels = []string{
		"phi3-mini-3.8b",
		"llama3-8b",
		"llama2-13b",
	}

	allGGUFFormats = []string{
		"q4_k_m.gguf",
		"q8_0.gguf",
	}

	lmcScenarios = []string{
		"s2-lmc-cold",
		"s3-lmc-warm",
	}

	remoteScenarios = []string{
		"s1-remote-cold",
	}
)

func TestISVCName_BasicConcatenation(t *testing.T) {
	got := ISVCName("llamacpp-default", "gguf-q4km", "remote", "llama3-8b")
	want := "llamacpp-default-gguf-q4km-remote-llama3-8b"
	if got != want {
		t.Errorf("ISVCName = %q, want %q", got, want)
	}
}

// Regression guard: model names like "phi3-mini-3.8b" contain a dot, which is
// not a valid character in a K8s resource name. Sanitisation must convert dots
// to hyphens. If `strings.ReplaceAll`'s return value is discarded (a common
// Go bug since strings are immutable), this test fails.
func TestISVCName_DotsConvertedToHyphens(t *testing.T) {
	got := ISVCName("llamacpp-default", "gguf-q4km", "lmc", "phi3-mini-3.8b")
	if strings.Contains(got, ".") {
		t.Errorf("ISVCName must not contain dots, got %q", got)
	}
	want := "llamacpp-default-gguf-q4km-lmc-phi3-mini-3-8b"
	if got != want {
		t.Errorf("ISVCName = %q, want %q", got, want)
	}
}

func TestISVCName_UnderscoresConvertedToHyphens(t *testing.T) {
	got := ISVCName("foo_bar", "baz", "remote", "qux")
	if strings.Contains(got, "_") {
		t.Errorf("ISVCName must not contain underscores, got %q", got)
	}
}

func TestISVCName_SlashesConvertedToHyphens(t *testing.T) {
	got := ISVCName("foo/bar", "baz", "remote", "qux")
	if strings.Contains(got, "/") {
		t.Errorf("ISVCName must not contain slashes, got %q", got)
	}
}

// RFC 1123 label values used in the pod selector cannot exceed 63 characters.
func TestISVCName_MaxLengthRespected(t *testing.T) {
	got := ISVCName(
		"a-very-long-runtime-name-that-exceeds-reason",
		"an-even-longer-format-definition-suffix",
		"remote",
		"and-a-model-name-for-good-measure",
	)
	if len(got) > 63 {
		t.Errorf("ISVCName must be <= 63 chars (RFC 1123 label limit), got %d: %q",
			len(got), got)
	}
}

// RFC 1123 also requires names to start and end with an alphanumeric.
func TestISVCName_StartsAndEndsAlphanumeric(t *testing.T) {
	got := ISVCName("a", "b", "c", "d")
	if len(got) == 0 {
		t.Fatal("ISVCName returned empty string")
	}
	if !isAlphanumeric(got[0]) {
		t.Errorf("ISVCName must start with alphanumeric, got %q", got)
	}
	if !isAlphanumeric(got[len(got)-1]) {
		t.Errorf("ISVCName must end with alphanumeric, got %q", got)
	}
}

func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// =============================================================================
// Render (integration against a minimal fixture)
// =============================================================================

// minimalConfig returns a small BenchConfig sufficient to exercise the
// renderer end-to-end without depending on configs/matrix-full.yaml on disk.
func minimalConfig(t *testing.T) *config.BenchConfig {
	t.Helper()
	return &config.BenchConfig{
		Testbed: config.TestbedConfig{
			Namespace: "test-bench",
		},
		Matrix: config.MatrixConfig{
			Runtimes: []config.RuntimeDef{
				{
					Name:         "llamacpp-default",
					RuntimeClass: "ClusterServingRuntime/llamacpp",
					Loader:       "default",
				},
			},
			Formats: []config.FormatDef{
				{Name: "gguf-q4km", Path: "s3://models/{model}/q4_k_m.gguf"},
			},
			Models: []config.ModelDef{
				{Name: "phi3-mini-3.8b", SizeGB: 7.6},
			},
			Caching: []config.CachingDef{
				{Name: "remote", LocalModelCache: false},
				{Name: "lmc", LocalModelCache: true},
			},
			Scenarios: []config.ScenarioDef{
				{Name: "s1-remote-cold", Caching: "remote", DropPageCache: true},
				{Name: "s2-lmc-cold", Caching: "lmc", DropPageCache: true},
			},
		},
	}
}

// renderedISVC is a parallel struct used to parse generated YAML in tests,
// deliberately not the production type. This catches schema mismatches if the
// production struct's YAML tags ever drift from what KServe expects.
type renderedISVC struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Labels      map[string]string `yaml:"labels"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Spec struct {
		Predictor struct {
			ServiceAccountName string `yaml:"serviceAccountName"`
			Model              struct {
				ModelFormat struct {
					Name string `yaml:"name"`
				} `yaml:"modelFormat"`
				Runtime    string `yaml:"runtime"`
				StorageURI string `yaml:"storageUri"`
				Env        []struct {
					Name  string `yaml:"name"`
					Value string `yaml:"value"`
				} `yaml:"env"`
				Resources struct {
					Limits map[string]string `yaml:"limits"`
				} `yaml:"resources"`
			} `yaml:"model"`
		} `yaml:"predictor"`
	} `yaml:"spec"`
}

func readISVC(t *testing.T, path string) renderedISVC {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	var isvc renderedISVC
	if err := yaml.Unmarshal(data, &isvc); err != nil {
		t.Fatalf("Unmarshal %s: %v", path, err)
	}
	return isvc
}

func renderTo(t *testing.T, cfg *config.BenchConfig) string {
	t.Helper()
	dir := t.TempDir()
	if err := NewRenderer(dir).Render(cfg); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	return dir
}

func TestRender_ProducesExpectedFileCount(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// 1 runtime × 1 format × 1 model × 2 caching = 2 ISVCs.
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected 2 ISVC files, got %d: %v", len(entries), names)
	}
}

func TestRender_FilesAreNamedCorrectly(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)

	expected := []string{
		"llamacpp-default-gguf-q4km-remote-phi3-mini-3-8b.yaml",
		"llamacpp-default-gguf-q4km-lmc-phi3-mini-3-8b.yaml",
	}
	for _, name := range expected {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected file %s, but: %v", name, err)
		}
	}
}

// Regression guard for the serviceAccountFor bug: the SA must be chosen
// based on the caching class semantics (remote vs lmc), not on URI prefix
// matching of the caching name (which never starts with s3://).
func TestRender_RemoteISVCUsesS3ServiceAccountAndS3Storage(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)

	isvc := readISVC(t, filepath.Join(dir, "llamacpp-default-gguf-q4km-remote-phi3-mini-3-8b.yaml"))

	if !strings.HasPrefix(isvc.Spec.Predictor.Model.StorageURI, "s3://") {
		t.Errorf("remote ISVC must use s3:// storageUri, got %q",
			isvc.Spec.Predictor.Model.StorageURI)
	}
	if isvc.Spec.Predictor.ServiceAccountName != "kserve-bench-s3" {
		t.Errorf("remote ISVC must use S3 SA 'kserve-bench-s3', got %q",
			isvc.Spec.Predictor.ServiceAccountName)
	}
}

func TestRender_LMCISVCUsesPVCServiceAccountAndPVCStorage(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)

	isvc := readISVC(t, filepath.Join(dir, "llamacpp-default-gguf-q4km-lmc-phi3-mini-3-8b.yaml"))

	if !strings.HasPrefix(isvc.Spec.Predictor.Model.StorageURI, "pvc://") {
		t.Errorf("lmc ISVC must use pvc:// storageUri, got %q",
			isvc.Spec.Predictor.Model.StorageURI)
	}
	if isvc.Spec.Predictor.ServiceAccountName != "kserve-bench-pvc" {
		t.Errorf("lmc ISVC must use PVC SA 'kserve-bench-pvc', got %q",
			isvc.Spec.Predictor.ServiceAccountName)
	}
}

// Multiple scenarios sharing the same caching class must produce a single
// ISVC. The full matrix has s2-lmc-cold and s3-lmc-warm both pointing at
// caching=lmc; both should collapse to one llamacpp-default-...-lmc-... file.
func TestRender_ScenariosSharingCachingAreDeduplicated(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Matrix.Scenarios = append(cfg.Matrix.Scenarios,
		config.ScenarioDef{Name: "s3-lmc-warm", Caching: "lmc", DropPageCache: false},
	)
	dir := renderTo(t, cfg)

	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("scenarios sharing caching=lmc must collapse to one ISVC; got %d files",
			len(entries))
	}
}

func TestRender_HasCorrectKindAndAPIVersion(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)
	entries, _ := os.ReadDir(dir)
	isvc := readISVC(t, filepath.Join(dir, entries[0].Name()))

	if isvc.Kind != "InferenceService" {
		t.Errorf("kind = %q, want 'InferenceService'", isvc.Kind)
	}
	if isvc.APIVersion != "serving.kserve.io/v1beta1" {
		t.Errorf("apiVersion = %q, want 'serving.kserve.io/v1beta1'", isvc.APIVersion)
	}
}

// τ for the M/G/1 queuing model (RQ4) must be present, set to the documented
// value (30s per the renderer guide), and identical across every ISVC.
func TestRender_TauAnnotationIs30s(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Fatal("no files rendered")
	}

	for _, e := range entries {
		isvc := readISVC(t, filepath.Join(dir, e.Name()))
		tau, ok := isvc.Metadata.Annotations["autoscaling.knative.dev/scaleToZeroPodRetentionPeriod"]
		if !ok {
			t.Errorf("%s: scaleToZeroPodRetentionPeriod annotation missing (τ for M/G/1)", e.Name())
			continue
		}
		if tau != "30s" {
			t.Errorf("%s: scaleToZeroPodRetentionPeriod = %q, want '30s'", e.Name(), tau)
		}
	}
}

func TestRender_LoaderEnvVarsPresent(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)
	entries, _ := os.ReadDir(dir)
	isvc := readISVC(t, filepath.Join(dir, entries[0].Name()))

	var hasLoader, hasArgs bool
	for _, e := range isvc.Spec.Predictor.Model.Env {
		if e.Name == "ANANSI_LOADER" {
			hasLoader = true
		}
		if e.Name == "ANANSI_LOADER_ARGS" {
			hasArgs = true
		}
	}
	if !hasLoader {
		t.Error("ANANSI_LOADER env var missing from rendered ISVC")
	}
	if !hasArgs {
		t.Error("ANANSI_LOADER_ARGS env var missing from rendered ISVC")
	}
}

// modelFormatFor / runtimeName extract the first hyphen-separated segment.
// "gguf-q4km" → "gguf"; "llamacpp-default" → "llamacpp".
// These must match the CSR's supportedModelFormats and metadata.name.
func TestRender_ModelFormatExtractsBeforeFirstHyphen(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)
	entries, _ := os.ReadDir(dir)
	isvc := readISVC(t, filepath.Join(dir, entries[0].Name()))

	if isvc.Spec.Predictor.Model.ModelFormat.Name != "gguf" {
		t.Errorf("modelFormat.name = %q, want 'gguf'",
			isvc.Spec.Predictor.Model.ModelFormat.Name)
	}
}

func TestRender_RuntimeRefExtractsBeforeFirstHyphen(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)
	entries, _ := os.ReadDir(dir)
	isvc := readISVC(t, filepath.Join(dir, entries[0].Name()))

	if isvc.Spec.Predictor.Model.Runtime != "llamacpp" {
		t.Errorf("predictor.model.runtime = %q, want 'llamacpp' (CSR name)",
			isvc.Spec.Predictor.Model.Runtime)
	}
}

// GPU resource limit must be present — without it the pod schedules on
// CPU-only nodes and CUDA kernels fail at startup.
func TestRender_GPUResourceLimitPresent(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)
	entries, _ := os.ReadDir(dir)
	isvc := readISVC(t, filepath.Join(dir, entries[0].Name()))

	gpu, ok := isvc.Spec.Predictor.Model.Resources.Limits["nvidia.com/gpu"]
	if !ok {
		t.Error("nvidia.com/gpu limit missing — pod won't get a GPU assigned")
	}
	if gpu != "1" {
		t.Errorf("nvidia.com/gpu = %q, want '1'", gpu)
	}
}

// Anansi labels let the user (or kubectl selectors) filter ISVCs by dimension.
func TestRender_AnansiLabelsPresent(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)
	entries, _ := os.ReadDir(dir)
	isvc := readISVC(t, filepath.Join(dir, entries[0].Name()))

	wantLabels := map[string]string{
		"anansi.runtime": "llamacpp-default",
		"anansi.format":  "gguf-q4km",
		"anansi.model":   "phi3-mini-3.8b",
	}
	for k, v := range wantLabels {
		got, ok := isvc.Metadata.Labels[k]
		if !ok {
			t.Errorf("label %q missing", k)
			continue
		}
		if got != v {
			t.Errorf("label %q = %q, want %q", k, got, v)
		}
	}
	// caching label varies by file; just confirm it exists.
	if _, ok := isvc.Metadata.Labels["anansi.caching"]; !ok {
		t.Error("label 'anansi.caching' missing")
	}
}

func TestRender_NamespaceFromConfig(t *testing.T) {
	cfg := minimalConfig(t)
	dir := renderTo(t, cfg)
	entries, _ := os.ReadDir(dir)
	isvc := readISVC(t, filepath.Join(dir, entries[0].Name()))

	if isvc.Metadata.Namespace != "test-bench" {
		t.Errorf("namespace = %q, want 'test-bench' (from cfg.Testbed.Namespace)",
			isvc.Metadata.Namespace)
	}
}

// Render must create the output directory if it doesn't already exist;
// otherwise the very first run fails with a useless "no such file or directory".
func TestRender_CreatesOutputDirIfMissing(t *testing.T) {
	parent := t.TempDir()
	sub := filepath.Join(parent, "does-not-exist-yet")
	cfg := minimalConfig(t)

	if err := NewRenderer(sub).Render(cfg); err != nil {
		t.Fatalf("Render must create missing output dir, got error: %v", err)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("output directory should exist after Render, got %v", err)
	}
}

// SaveRender's error should propagate. We trigger a write failure by
// pointing the renderer at a path that exists as a file (so creating a
// directory at the same path fails), and assert Render returns an error.
func TestRender_PropagatesWriteErrors(t *testing.T) {
	parent := t.TempDir()
	conflictPath := filepath.Join(parent, "conflict")
	// Create a regular file at the path the renderer will try to use as a dir.
	if err := os.WriteFile(conflictPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg := minimalConfig(t)

	err := NewRenderer(conflictPath).Render(cfg)
	if err == nil {
		t.Error("Render must return an error when output dir cannot be created or written to")
	}
}

// TestStorageURI_PVC_IsRootOnly verifies that for every LMC scenario,
// the rendered storageUri is mount-only — no per-file path, no per-model
// path. This is the empirically-discovered constraint behind D-018: KServe
// interprets pvc:// as "mount this PVC", not "fetch this specific file".
func TestStorageURI_PVC_IsRootOnly(t *testing.T) {
	const want = "pvc://models-cache"

	for _, scenario := range lmcScenarios {
		for _, model := range allModels {
			for _, format := range allGGUFFormats {
				name := scenario + "/" + model + "/" + format
				t.Run(name, func(t *testing.T) {
					got := StorageURI(format, model, scenario)
					if got != want {
						t.Errorf("StorageURI(%q, %q, %q) = %q; want %q (pvc URIs must be mount-only)",
							format, model, scenario, got, want)
					}
				})
			}
		}
	}
}

// TestStorageURI_S3_IncludesFilePath verifies that for s1-remote-cold,
// the URI points at the specific object in the models bucket — because
// KServe's S3 StorageInitializer downloads exactly that object to
// /mnt/models. This is the inverse semantics of pvc:// and must be
// preserved.
func TestStorageURI_S3_IncludesFilePath(t *testing.T) {
	for _, scenario := range remoteScenarios {
		for _, model := range allModels {
			for _, format := range allGGUFFormats {
				name := scenario + "/" + model + "/" + format
				t.Run(name, func(t *testing.T) {
					want := "s3://models/" + model + "/" + format
					got := StorageURI(format, model, scenario)
					if got != want {
						t.Errorf("StorageURI(%q, %q, %q) = %q; want %q (s3 URIs must address the specific object)",
							format, model, scenario, got, want)
					}
				})
			}
		}
	}
}

// TestModelPath_PVCNested verifies that for LMC scenarios, MODEL_PATH
// includes the <model>/<format> nesting — because the PVC contains
// every model and the entrypoint needs the specific one. This is the
// asymmetric pair to TestStorageURI_PVC_IsRootOnly: the URI loses the
// path so MODEL_PATH must carry it instead.
func TestModelPath_PVCNested(t *testing.T) {
	for _, scenario := range lmcScenarios {
		for _, model := range allModels {
			for _, format := range allGGUFFormats {
				name := scenario + "/" + model + "/" + format
				t.Run(name, func(t *testing.T) {
					want := "/mnt/models/" + model + "/" + format
					got := ModelPath(format, model, scenario)
					if got != want {
						t.Errorf("ModelPath(%q, %q, %q) = %q; want %q (LMC paths must include model subdir)",
							format, model, scenario, got, want)
					}
				})
			}
		}
	}
}

// TestModelPath_S3Basename verifies that for s1-remote-cold, MODEL_PATH
// is just /mnt/models/<format> — because the S3 StorageInitializer
// downloads the single addressed object to /mnt/models/ with the original
// basename, no per-model subdirectory.
func TestModelPath_S3Basename(t *testing.T) {
	for _, scenario := range remoteScenarios {
		for _, model := range allModels {
			for _, format := range allGGUFFormats {
				name := scenario + "/" + model + "/" + format
				t.Run(name, func(t *testing.T) {
					want := "/mnt/models/" + format
					got := ModelPath(format, model, scenario)
					if got != want {
						t.Errorf("ModelPath(%q, %q, %q) = %q; want %q (s3 paths use basename only)",
							format, model, scenario, got, want)
					}
				})
			}
		}
	}
}

// TestStorageURI_ModelPath_AreConsistent is a meta-test: it ensures the
// two functions agree on the file the runtime will actually load. Given
// StorageURI and ModelPath for the same (format, model, scenario), the
// runtime must be able to find the file at MODEL_PATH after KServe's
// storage layer materialises the URI.
//
// We can't run KServe in a unit test, but we can check the invariant
// that MODEL_PATH ends in the format basename — a necessary condition
// for the runtime to find the file regardless of scenario.
func TestStorageURI_ModelPath_AreConsistent(t *testing.T) {
	allScenarios := append([]string{}, lmcScenarios...)
	allScenarios = append(allScenarios, remoteScenarios...)

	for _, scenario := range allScenarios {
		for _, model := range allModels {
			for _, format := range allGGUFFormats {
				name := scenario + "/" + model + "/" + format
				t.Run(name, func(t *testing.T) {
					path := ModelPath(format, model, scenario)
					// MODEL_PATH must end with the format basename; otherwise
					// the entrypoint can never find the file.
					wantSuffix := "/" + format
					if len(path) < len(wantSuffix) || path[len(path)-len(wantSuffix):] != wantSuffix {
						t.Errorf("ModelPath(%q, %q, %q) = %q; expected to end with %q",
							format, model, scenario, path, wantSuffix)
					}
				})
			}
		}
	}
}
