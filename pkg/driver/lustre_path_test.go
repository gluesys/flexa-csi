package driver

import "testing"

func TestLustreSubDir(t *testing.T) {
	for _, c := range []struct{ fs, ns, want string }{
		{"lustre", "gluesys05", "k8s/gluesys05"},
		{"lustre", "", "k8s"},
		{"zfs", "gluesys05", ""},
	} {
		if got := lustreSubDir(c.fs, c.ns); got != c.want {
			t.Errorf("lustreSubDir(%q, %q) = %q, want %q", c.fs, c.ns, got, c.want)
		}
	}
}

func TestLustreMountSource(t *testing.T) {
	const mgs = "10.0.0.1@o2ib:10.0.0.2@o2ib"
	for _, c := range []struct{ baseDir, want string }{
		{"/lustre/agent/fs1/k8s/ns1/pvc-1", mgs + ":/fs1/k8s/ns1/pvc-1"},
		{"/lustre/agent/fs1/pvc-1", mgs + ":/fs1/pvc-1"}, // volume created before subDir
		{"", mgs + ":/fs1/pvc-1"},
		{"/lustre/agent/fs2/k8s/ns1/pvc-1", mgs + ":/fs1/pvc-1"}, // other filesystem
		{"/lustre/agent/fs1/../fs2/pvc-1", mgs + ":/fs1/pvc-1"},
		{"/lustre/agent/fs1/k8s//pvc-1", mgs + ":/fs1/pvc-1"},
		{"/lustre/agent/fs1/", mgs + ":/fs1/pvc-1"},
	} {
		if got := lustreMountSource(mgs, "fs1", "pvc-1", c.baseDir); got != c.want {
			t.Errorf("baseDir %q: source = %q, want %q", c.baseDir, got, c.want)
		}
	}
}
