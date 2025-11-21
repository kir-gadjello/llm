package main

import (
	"testing"
)

func TestResolveModelConfig(t *testing.T) {
	tempA := 0.5
	tempB := 0.7

	cfg := &ConfigFile{
		Models: map[string]ModelConfig{
			"base": {
				Temperature: &tempA,
				ExtraBody: map[string]interface{}{
					"param1": "value1",
					"nested": map[string]interface{}{
						"a": 1,
						"b": 2,
					},
				},
			},
			"child": {
				Extend:      &[]string{"base"}[0],
				Temperature: &tempB,
				ExtraBody: map[string]interface{}{
					"param2": "value2",
					"nested": map[string]interface{}{
						"b": 3, // Override
						"c": 4, // Add
					},
				},
			},
			"grandchild": {
				Extend: &[]string{"child"}[0],
				ExtraBody: map[string]interface{}{
					"param3": "value3",
				},
			},
			"cycle-a": {
				Extend: &[]string{"cycle-b"}[0],
			},
			"cycle-b": {
				Extend: &[]string{"cycle-a"}[0],
			},
		},
	}

	t.Run("Base Config", func(t *testing.T) {
		res, err := resolveModelConfig(cfg, "base")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if *res.Temperature != tempA {
			t.Errorf("expected temp %v, got %v", tempA, *res.Temperature)
		}
		if res.ExtraBody["param1"] != "value1" {
			t.Errorf("expected param1=value1, got %v", res.ExtraBody["param1"])
		}
	})

	t.Run("Child Config", func(t *testing.T) {
		res, err := resolveModelConfig(cfg, "child")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if *res.Temperature != tempB {
			t.Errorf("expected temp %v, got %v", tempB, *res.Temperature)
		}
		if res.ExtraBody["param1"] != "value1" {
			t.Errorf("expected inherited param1=value1, got %v", res.ExtraBody["param1"])
		}
		if res.ExtraBody["param2"] != "value2" {
			t.Errorf("expected param2=value2, got %v", res.ExtraBody["param2"])
		}

		nested := res.ExtraBody["nested"].(map[string]interface{})
		if nested["a"] != 1 {
			t.Errorf("expected nested.a=1, got %v", nested["a"])
		}
		if nested["b"] != 3 {
			t.Errorf("expected nested.b=3, got %v", nested["b"])
		}
		if nested["c"] != 4 {
			t.Errorf("expected nested.c=4, got %v", nested["c"])
		}
	})

	t.Run("Grandchild Config", func(t *testing.T) {
		res, err := resolveModelConfig(cfg, "grandchild")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if *res.Temperature != tempB {
			t.Errorf("expected inherited temp %v, got %v", tempB, *res.Temperature)
		}
		if res.ExtraBody["param3"] != "value3" {
			t.Errorf("expected param3=value3, got %v", res.ExtraBody["param3"])
		}
		if res.ExtraBody["param1"] != "value1" {
			t.Errorf("expected inherited param1=value1, got %v", res.ExtraBody["param1"])
		}
	})

	t.Run("Circular Dependency", func(t *testing.T) {
		_, err := resolveModelConfig(cfg, "cycle-a")
		if err == nil {
			t.Fatal("expected error for circular dependency, got nil")
		}
	})
}
