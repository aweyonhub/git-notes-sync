package main

import (
	"reflect"
	"testing"
)

func TestExpandGnmAlias(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"status"}, []string{"map", "status"}},
		{[]string{"config", "list"}, []string{"map-config", "list"}},
		{nil, []string{"map"}},
	}
	for _, test := range tests {
		if got := expandGnmAlias("gnm.exe", test.args); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("expandGnmAlias(%v) = %v, want %v", test.args, got, test.want)
		}
	}
}
