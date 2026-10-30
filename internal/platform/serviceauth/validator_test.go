package serviceauth

import "testing"

func TestValidateAuthorization(t *testing.T) {
	validator := NewValidator("service-secret")

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "valid", header: "Bearer service-secret", want: true},
		{name: "wrong scheme", header: "tma service-secret", want: false},
		{name: "wrong token", header: "Bearer wrong-secret", want: false},
		{name: "missing token", header: "Bearer", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validator.ValidateAuthorization(test.header); got != test.want {
				t.Errorf("ValidateAuthorization() = %t, want %t", got, test.want)
			}
		})
	}
}
