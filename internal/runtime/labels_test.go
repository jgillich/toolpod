package runtime

import "testing"

func TestServiceNetworkAlias(t *testing.T) {
	if got := ServiceNetworkAlias("postgres-main"); got != "tpd-svc-postgres-main" {
		t.Fatalf("alias = %q", got)
	}
}

func TestServiceHostEnvName(t *testing.T) {
	if got := ServiceHostEnvName("postgres-main"); got != "TPD_SERVICE_POSTGRES_MAIN_HOST" {
		t.Fatalf("environment name = %q", got)
	}
}

func TestServiceNamesInjective(t *testing.T) {
	names := []string{"a", "a-b", "b", "ab"}
	aliases := make(map[string]string)
	envs := make(map[string]string)
	for _, name := range names {
		alias := ServiceNetworkAlias(name)
		env := ServiceHostEnvName(name)
		if prev, ok := aliases[alias]; ok {
			t.Fatalf("alias %q shared by %q and %q", alias, prev, name)
		}
		aliases[alias] = name
		if prev, ok := envs[env]; ok {
			t.Fatalf("environment name %q shared by %q and %q", env, prev, name)
		}
		envs[env] = name
	}
}

func TestServiceCaseVariantsCollide(t *testing.T) {
	if got := ServiceHostEnvName("Redis"); got != "TPD_SERVICE_REDIS_HOST" {
		t.Fatalf("environment name = %q", got)
	}
	if ServiceHostEnvName("Redis") != ServiceHostEnvName("redis") {
		t.Error("case variants must collapse to one env name, which the lowercase-only grammar prevents")
	}
}
