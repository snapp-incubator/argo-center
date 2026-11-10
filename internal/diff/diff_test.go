package diff

import (
	"slices"
	"strings"
	"testing"

	"github.com/snapp-incubator/argo-center/internal/argocd"
)

const liveDeploy = `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"x","uid":"u1","resourceVersion":"9",
"managedFields":[{"manager":"kubectl"}],"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}","team":"a"}},
"spec":{"replicas":3,"template":{"spec":{"containers":[{"name":"api","image":"api:1.41","env":[{"name":"A","value":"1"},{"name":"OLD","value":"x"}]}]}}},
"status":{"readyReplicas":3}}`

const targetDeploy = `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"x","uid":"u1","resourceVersion":"9",
"labels":{"app.kubernetes.io/name":"api"},"annotations":{"argocd.argoproj.io/sync-wave":"1","team":"a"}},
"spec":{"replicas":4,"template":{"spec":{"containers":[{"name":"api","image":"api:1.42","env":[{"name":"A","value":"1"}]}]}}},
"status":{"readyReplicas":3}}`

func TestComputeModified(t *testing.T) {
	res, err := Compute([]argocd.ManagedResource{{
		Group: "apps", Kind: "Deployment", Namespace: "x", Name: "api", Modified: true,
		TargetState: targetDeploy, LiveState: liveDeploy, NormalizedLiveState: liveDeploy, PredictedLiveState: targetDeploy,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != StatusModified {
		t.Fatalf("got %+v", res)
	}
	want := []Change{
		{`metadata.annotations["argocd.argoproj.io/sync-wave"]`, OpAdded},
		{"metadata.labels", OpAdded},
		{"spec.replicas", OpChanged},
		{"spec.template.spec.containers[api].env[OLD]", OpRemoved},
		{"spec.template.spec.containers[api].image", OpChanged},
	}
	if !slices.Equal(res[0].Changes, want) {
		t.Errorf("changes = %v\nwant      %v", res[0].Changes, want)
	}
	d := res[0].UnifiedDiff
	for _, line := range []string{"-  replicas: 3\n", "+  replicas: 4\n", "-          image: api:1.41\n", "+          image: api:1.42\n", "-            - name: OLD\n"} {
		if !strings.Contains(d, line) {
			t.Errorf("unified diff lacks %q:\n%s", line, d)
		}
	}
	for _, noise := range []string{"managedFields", "readyReplicas", "last-applied", "+++", "---"} {
		if strings.Contains(d, noise) {
			t.Errorf("unified diff contains %q:\n%s", noise, d)
		}
	}
}

func TestComputeStatuses(t *testing.T) {
	cm := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"},"data":{"k":"v"}}`
	res, err := Compute([]argocd.ManagedResource{
		{Kind: "Service", Name: "same", TargetState: cm, NormalizedLiveState: cm, PredictedLiveState: cm},
		{Kind: "ConfigMap", Name: "new", Modified: true, TargetState: cm, LiveState: "null", NormalizedLiveState: "null", PredictedLiveState: cm},
		{Kind: "ConfigMap", Name: "gone", TargetState: "null", LiveState: cm, NormalizedLiveState: cm, PredictedLiveState: "null"},
		{Kind: "Job", Name: "hook", Hook: true, Modified: true, TargetState: cm, NormalizedLiveState: "null", PredictedLiveState: cm},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range res {
		got[r.Name] = r.Status
	}
	if len(got) != 2 || got["new"] != StatusMissing || got["gone"] != StatusExtra {
		t.Fatalf("statuses = %v", got)
	}
	for _, r := range res {
		sign := "+"
		if r.Status == StatusExtra {
			sign = "-"
		}
		if !strings.Contains(r.UnifiedDiff, sign+"  k: v\n") {
			t.Errorf("%s: unified diff:\n%s", r.Name, r.UnifiedDiff)
		}
	}
}

// A resource can be marked modified while only ignored fields differ. Its changes must still be
// an empty list in JSON: the UI reads its length.
func TestModifiedWithoutChangesIsNotNull(t *testing.T) {
	live := `{"kind":"ConfigMap","metadata":{"name":"c","resourceVersion":"1"},"data":{"a":"1"}}`
	target := `{"kind":"ConfigMap","metadata":{"name":"c","resourceVersion":"2"},"data":{"a":"1"}}`
	res, err := Compute([]argocd.ManagedResource{{
		Kind: "ConfigMap", Name: "c", Modified: true,
		TargetState: target, NormalizedLiveState: live, PredictedLiveState: target,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Changes == nil {
		t.Fatalf("changes must be an empty list, got %+v", res)
	}
}

func TestPathsLists(t *testing.T) {
	a := map[string]any{"args": []any{"--a", "--b"}, "ports": []any{map[string]any{"name": "http", "port": int64(80)}}}
	b := map[string]any{"args": []any{"--a", "--c", "--d"}, "ports": []any{map[string]any{"name": "http", "port": int64(8080)}}}
	want := []Change{{"args[1]", OpChanged}, {"args[2]", OpAdded}, {"ports[http].port", OpChanged}}
	if got := Paths(a, b); !slices.Equal(got, want) {
		t.Errorf("Paths = %v, want %v", got, want)
	}
}

func TestNumbersStayUnquoted(t *testing.T) {
	obj, err := parse(`{"spec":{"port":8080,"ratio":0.5,"big":1000000}}`)
	if err != nil {
		t.Fatal(err)
	}
	y, _ := toYAML(obj)
	for _, want := range []string{"port: 8080", "ratio: 0.5", "big: 1000000"} {
		if !strings.Contains(y, want) {
			t.Errorf("YAML lacks %q:\n%s", want, y)
		}
	}
}

func TestMultilineStringsShowAsBlocks(t *testing.T) {
	cm := func(data string) map[string]any {
		return map[string]any{"kind": "ConfigMap", "data": map[string]any{"app.yaml": data}}
	}
	// Trailing spaces and CRLFs would otherwise force a single quoted line.
	d, _, err := unified(cm("a: 1 \r\nb: 2\r\nc: 3\n"), cm("a: 1 \r\nb: 9\r\nc: 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "-    b: 2\n") || !strings.Contains(d, "+    b: 9\n") || strings.Contains(d, `\n`) {
		t.Fatalf("want a line-by-line block diff, got:\n%s", d)
	}

	// A whitespace-only change stays visible: the raw strings are kept.
	d, _, err = unified(cm("a: 1 \nb: 2\n"), cm("a: 1\nb: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, `-  app.yaml: "a: 1 \nb: 2\n"`) {
		t.Fatalf("whitespace-only change hidden:\n%s", d)
	}
}
