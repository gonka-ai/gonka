package transport

import "testing"

func TestGetTransport_SameURL_Shared(t *testing.T) {
	t1 := getTransport("http://localhost:8080")
	t2 := getTransport("http://localhost:8080")
	if t1 != t2 {
		t.Fatal("same URL should return same transport pointer")
	}
}

func TestGetTransport_DifferentURL_Distinct(t *testing.T) {
	t1 := getTransport("http://host-a:8080")
	t2 := getTransport("http://host-b:8080")
	if t1 == t2 {
		t.Fatal("different URLs should return different transport pointers")
	}
}
