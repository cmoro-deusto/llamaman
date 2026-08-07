package hf

// RepoFile is one file entry of a repo tree (DESIGN §16.2). OID is the
// LFS sha256 when the file is LFS-stored, else the plain oid — the
// value the downloader verifies against.
type RepoFile struct {
	Path string
	Size int64
	OID  string
}

// RepoMeta is the metadata returned by GET /api/models/{repo}. The
// browser (item 7) will extend this shape; keep it minimal for now.
type RepoMeta struct {
	ID        string
	SHA       string
	Downloads int64
	Likes     int64
	Tags      []string
}
