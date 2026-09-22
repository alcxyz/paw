// Package backup writes and restores the offline workspace backup format.
// It does not publish artifacts or interact with a running workspace.
package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	formatVersion       = 1
	metadataName        = "metadata.json"
	completionName      = "complete.json"
	maxControlBytes     = 64 << 10
	maxMetadataField    = 4 << 10
	maxArchivePath      = 4 << 10
	maxEntries          = 1_000_000
	maxVolumeBytes      = int64(20 << 30)
	maxArchiveBytes     = int64(48 << 30)
	allowedArchiveModes = int64(0o3777)
)

// Metadata identifies the runtime whose stopped state is in a backup.
type Metadata struct {
	Image    string
	ImageID  string
	Profile  string
	Provider string
}

// WriteRequest selects the two offline volume roots and their runtime identity.
type WriteRequest struct {
	Metadata  Metadata
	StateRoot string
	WorkRoot  string
}

// RestoreRequest selects two existing empty roots for restored volume data.
type RestoreRequest struct {
	StateRoot string
	WorkRoot  string
}

type metadataDocument struct {
	FormatVersion int    `json:"formatVersion"`
	Image         string `json:"image"`
	ImageID       string `json:"imageID"`
	Profile       string `json:"profile"`
	Provider      string `json:"provider"`
}

type completionDocument struct {
	FormatVersion int    `json:"formatVersion"`
	StateEntries  int    `json:"stateEntries"`
	StateBytes    int64  `json:"stateBytes"`
	WorkEntries   int    `json:"workEntries"`
	WorkBytes     int64  `json:"workBytes"`
	Digest        string `json:"digest"`
}

type volumeStats struct {
	entries int
	bytes   int64
}

type archiveStats struct {
	state volumeStats
	work  volumeStats
}

// Write writes one complete v1 tar stream. The caller owns private staging and
// must discard the destination if Write returns an error. Relative symbolic
// links are preserved, but targets containing a .. component are unsupported.
func Write(ctx context.Context, dst io.Writer, request WriteRequest) error {
	if ctx == nil || dst == nil || !validMetadata(request.Metadata) {
		return errors.New("backup write request is invalid")
	}
	stateInfo, err := validSourceRoot(request.StateRoot)
	if err != nil {
		return errors.New("backup state root is invalid")
	}
	workInfo, err := validSourceRoot(request.WorkRoot)
	if err != nil || os.SameFile(stateInfo, workInfo) {
		return errors.New("backup work root is invalid")
	}

	document := metadataDocument{
		FormatVersion: formatVersion,
		Image:         request.Metadata.Image,
		ImageID:       request.Metadata.ImageID,
		Profile:       request.Metadata.Profile,
		Provider:      request.Metadata.Provider,
	}
	metadataBytes, err := json.Marshal(document)
	if err != nil {
		return errors.New("encode backup metadata")
	}

	digest := sha256.New()
	tw := tar.NewWriter(&contextWriter{ctx: ctx, writer: dst})
	if err := writeControlFile(tw, metadataName, metadataBytes); err != nil {
		return writeError(ctx)
	}
	hashEntryHeader(digest, metadataName, tar.TypeReg, 0o600, "", int64(len(metadataBytes)))
	_, _ = digest.Write(metadataBytes)

	var stats archiveStats
	if err := writeVolume(ctx, tw, digest, "state", request.StateRoot, &stats.state); err != nil {
		return err
	}
	if err := writeVolume(ctx, tw, digest, "work", request.WorkRoot, &stats.work); err != nil {
		return err
	}
	completion := completionDocument{
		FormatVersion: formatVersion,
		StateEntries:  stats.state.entries,
		StateBytes:    stats.state.bytes,
		WorkEntries:   stats.work.entries,
		WorkBytes:     stats.work.bytes,
		Digest:        hex.EncodeToString(digest.Sum(nil)),
	}
	completionBytes, err := json.Marshal(completion)
	if err != nil {
		return errors.New("encode backup completion record")
	}
	if err := writeControlFile(tw, completionName, completionBytes); err != nil {
		return writeError(ctx)
	}
	if err := tw.Close(); err != nil {
		return writeError(ctx)
	}
	return nil
}

func validSourceRoot(root string) (os.FileInfo, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("invalid path")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSetuid != 0 {
		return nil, errors.New("invalid directory")
	}
	return info, nil
}

func writeControlFile(tw *tar.Writer, name string, content []byte) error {
	header := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(content))}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

func writeVolume(
	ctx context.Context,
	tw *tar.Writer,
	digest hash.Hash,
	volume string,
	root string,
	stats *volumeStats,
) error {
	err := filepath.Walk(root, func(filePath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return errors.New("inspect backup source")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if stats.entries >= maxEntries {
			return errors.New("backup source has too many entries")
		}
		if info.Mode()&os.ModeSetuid != 0 || info.Mode()&os.ModeSetgid != 0 && !info.IsDir() {
			return errors.New("backup source has an unsupported mode")
		}

		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return errors.New("inspect backup source")
		}
		name := volume
		if relative != "." {
			name += "/" + filepath.ToSlash(relative)
		}
		if !validArchiveName(name) {
			return errors.New("backup source has an unsupported path")
		}

		header := &tar.Header{Name: name, Mode: archiveMode(info.Mode())}
		var file *os.File
		switch {
		case info.IsDir():
			header.Typeflag = tar.TypeDir
		case info.Mode().IsRegular():
			header.Typeflag = tar.TypeReg
			header.Size = info.Size()
			if header.Size < 0 || header.Size > maxVolumeBytes-stats.bytes {
				return errors.New("backup volume content exceeds the format limit")
			}
			file, err = os.Open(filePath)
			if err != nil {
				return errors.New("read backup source")
			}
			openedInfo, statErr := file.Stat()
			if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() != info.Size() {
				_ = file.Close()
				return errors.New("backup source changed while being read")
			}
		case info.Mode()&os.ModeSymlink != 0:
			header.Typeflag = tar.TypeSymlink
			header.Linkname, err = os.Readlink(filePath)
			if err != nil || !safeSymlink(name, header.Linkname) {
				return errors.New("backup source has an unsafe symbolic link")
			}
		default:
			return errors.New("backup source has an unsupported file type")
		}

		if err := tw.WriteHeader(header); err != nil {
			if file != nil {
				_ = file.Close()
			}
			return writeError(ctx)
		}
		hashEntryHeader(digest, header.Name, header.Typeflag, header.Mode, header.Linkname, header.Size)
		if file != nil {
			_, copyErr := io.CopyN(tw, io.TeeReader(&contextReader{ctx: ctx, reader: file}, digest), header.Size)
			currentInfo, statErr := file.Stat()
			closeErr := file.Close()
			if copyErr != nil {
				return writeError(ctx)
			}
			if statErr != nil || closeErr != nil || !os.SameFile(info, currentInfo) ||
				currentInfo.Size() != info.Size() || !currentInfo.ModTime().Equal(info.ModTime()) {
				return errors.New("backup source changed while being read")
			}
			stats.bytes += header.Size
		}
		stats.entries++
		return nil
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return errors.New("backup write cancelled")
	}
	return err
}

func archiveMode(mode os.FileMode) int64 {
	value := int64(mode.Perm())
	if mode&os.ModeSticky != 0 {
		value |= 0o1000
	}
	if mode&os.ModeSetgid != 0 {
		value |= 0o2000
	}
	return value
}

func writeError(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("backup write cancelled")
	}
	return errors.New("write backup archive")
}

// Verify consumes and validates one complete backup without extracting it.
func Verify(ctx context.Context, src io.Reader) (Metadata, error) {
	return process(ctx, src, nil)
}

// Restore validates and extracts one backup into two existing empty roots. The
// caller must guarantee that no other process writes those roots. Mount-root
// permissions remain environment-owned; modes are restored for entries below
// each root. If an error occurs after extraction begins, partial output is
// retained for inspection.
func Restore(ctx context.Context, src io.Reader, request RestoreRequest) (Metadata, error) {
	target, err := newRestoreTarget(request)
	if err != nil {
		return Metadata{}, err
	}
	metadata, processErr := process(ctx, src, target)
	if closeErr := target.close(); processErr == nil && closeErr != nil {
		return Metadata{}, closeErr
	}
	return metadata, processErr
}

type restoreTarget struct {
	state *os.Root
	work  *os.Root
	modes []restoredMode
}

type restoredMode struct {
	volume   string
	relative string
	mode     os.FileMode
}

func newRestoreTarget(request RestoreRequest) (*restoreTarget, error) {
	stateInfo, err := validEmptyRoot(request.StateRoot)
	if err != nil {
		return nil, errors.New("restore state root must be an existing empty directory")
	}
	workInfo, err := validEmptyRoot(request.WorkRoot)
	if err != nil || os.SameFile(stateInfo, workInfo) {
		return nil, errors.New("restore work root must be a distinct existing empty directory")
	}
	stateRoot, err := os.OpenRoot(request.StateRoot)
	if err != nil {
		return nil, errors.New("open restore state root")
	}
	workRoot, err := os.OpenRoot(request.WorkRoot)
	if err != nil {
		_ = stateRoot.Close()
		return nil, errors.New("open restore work root")
	}
	return &restoreTarget{state: stateRoot, work: workRoot}, nil
}

func validEmptyRoot(root string) (os.FileInfo, error) {
	info, err := validSourceRoot(root)
	if err != nil {
		return nil, err
	}
	directory, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	_, err = directory.Readdirnames(1)
	if !errors.Is(err, io.EOF) {
		return nil, errors.New("directory is not empty")
	}
	return info, nil
}

func (target *restoreTarget) begin(header *tar.Header, volume, relative string) (*os.File, error) {
	root := target.state
	if volume == "work" {
		root = target.work
	}
	mode := fileMode(header.Mode)
	localName := filepath.FromSlash(relative)
	if relative == "" {
		return nil, nil
	}
	switch header.Typeflag {
	case tar.TypeDir:
		if err := root.Mkdir(localName, 0o700); err != nil {
			return nil, errors.New("create restored directory")
		}
		target.modes = append(target.modes, restoredMode{volume: volume, relative: localName, mode: mode})
		return nil, nil
	case tar.TypeReg, tar.TypeRegA:
		file, err := root.OpenFile(localName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, errors.New("create restored file")
		}
		target.modes = append(target.modes, restoredMode{volume: volume, relative: localName, mode: mode})
		return file, nil
	case tar.TypeSymlink:
		if err := root.Symlink(header.Linkname, localName); err != nil {
			return nil, errors.New("create restored symbolic link")
		}
		return nil, nil
	default:
		return nil, errors.New("unsupported restored file type")
	}
}

func (target *restoreTarget) finish() error {
	for index := len(target.modes) - 1; index >= 0; index-- {
		entry := target.modes[index]
		root := target.state
		if entry.volume == "work" {
			root = target.work
		}
		if err := root.Chmod(entry.relative, entry.mode); err != nil {
			return errors.New("apply restored permissions")
		}
	}
	return nil
}

func (target *restoreTarget) close() error {
	stateErr := target.state.Close()
	workErr := target.work.Close()
	if stateErr != nil || workErr != nil {
		return errors.New("close restore roots")
	}
	return nil
}

func fileMode(mode int64) os.FileMode {
	value := os.FileMode(mode & 0o777)
	if mode&0o1000 != 0 {
		value |= os.ModeSticky
	}
	if mode&0o2000 != 0 {
		value |= os.ModeSetgid
	}
	return value
}

type archivePhase int

const (
	phaseState archivePhase = iota
	phaseWork
	phaseComplete
)

func process(ctx context.Context, src io.Reader, target *restoreTarget) (Metadata, error) {
	if ctx == nil || src == nil {
		return Metadata{}, errors.New("backup input is invalid")
	}
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, reader: src}, N: maxArchiveBytes + 1}
	tr := tar.NewReader(limited)
	digest := sha256.New()

	header, err := tr.Next()
	if err != nil || !validControlHeader(header, metadataName) {
		return Metadata{}, archiveError(ctx)
	}
	metadataBytes, err := readSmallEntry(tr, header.Size)
	if err != nil {
		return Metadata{}, archiveError(ctx)
	}
	hashEntryHeader(digest, header.Name, header.Typeflag, header.Mode, header.Linkname, header.Size)
	_, _ = digest.Write(metadataBytes)
	metadata, err := decodeMetadata(metadataBytes)
	if err != nil {
		return Metadata{}, errors.New("backup metadata is invalid")
	}

	seen := make(map[string]byte)
	stats := archiveStats{}
	phase := phaseState
	for {
		header, err = tr.Next()
		if err != nil {
			return Metadata{}, archiveError(ctx)
		}
		if header.Name == completionName {
			if phase != phaseWork || seen["state"] != tar.TypeDir || seen["work"] != tar.TypeDir ||
				!validControlHeader(header, completionName) {
				return Metadata{}, errors.New("backup completion record is invalid")
			}
			phase = phaseComplete
			break
		}

		volume, relative, nextPhase, err := validateDataHeader(header, phase, seen, &stats)
		if err != nil {
			return Metadata{}, err
		}
		phase = nextPhase
		hashEntryHeader(digest, header.Name, header.Typeflag, header.Mode, header.Linkname, header.Size)

		var output io.Writer = digest
		var restored *os.File
		if target != nil {
			restored, err = target.begin(header, volume, relative)
			if err != nil {
				return Metadata{}, errors.New("restore backup entry failed")
			}
			if restored != nil {
				output = io.MultiWriter(digest, restored)
			}
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			_, err = io.CopyN(output, tr, header.Size)
		}
		if restored != nil {
			closeErr := restored.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return Metadata{}, archiveError(ctx)
		}
	}

	completionBytes, err := readSmallEntry(tr, header.Size)
	if err != nil {
		return Metadata{}, archiveError(ctx)
	}
	if err := validateCompletion(completionBytes, stats, digest.Sum(nil)); err != nil {
		return Metadata{}, err
	}
	if next, nextErr := tr.Next(); nextErr != io.EOF || next != nil {
		return Metadata{}, errors.New("backup archive has content after its completion record")
	}
	var trailing [1]byte
	if count, trailingErr := limited.Read(trailing[:]); count != 0 || trailingErr != io.EOF {
		return Metadata{}, errors.New("backup archive has trailing data or exceeds the format limit")
	}
	if limited.N <= 0 {
		return Metadata{}, errors.New("backup archive exceeds the format limit")
	}
	if target != nil {
		if err := target.finish(); err != nil {
			return Metadata{}, err
		}
	}
	return metadata, nil
}

func validateDataHeader(
	header *tar.Header,
	phase archivePhase,
	seen map[string]byte,
	stats *archiveStats,
) (string, string, archivePhase, error) {
	if header == nil || !validArchiveName(header.Name) || header.Size < 0 ||
		header.Mode < 0 || header.Mode&^allowedArchiveModes != 0 ||
		header.Mode&0o4000 != 0 || header.Mode&0o2000 != 0 && header.Typeflag != tar.TypeDir ||
		!validExtendedHeader(header) {
		return "", "", phase, errors.New("backup archive entry is invalid")
	}
	if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg &&
		header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeSymlink {
		return "", "", phase, errors.New("backup archive contains an unsupported file type")
	}
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Size != 0 {
		return "", "", phase, errors.New("backup archive entry has an invalid size")
	}
	if header.Typeflag != tar.TypeSymlink && header.Linkname != "" {
		return "", "", phase, errors.New("backup archive entry has an unexpected link target")
	}

	volume, relative, ok := strings.Cut(header.Name, "/")
	if !ok {
		volume, relative = header.Name, ""
	}
	if volume != "state" && volume != "work" {
		return "", "", phase, errors.New("backup archive entry is outside a volume")
	}
	if phase == phaseState && volume == "work" {
		phase = phaseWork
	}
	if phase == phaseWork && volume == "state" {
		return "", "", phase, errors.New("backup archive volume records are out of order")
	}
	if relative == "" && header.Typeflag != tar.TypeDir {
		return "", "", phase, errors.New("backup archive volume root is invalid")
	}
	if relative != "" {
		parent := path.Dir(header.Name)
		if seen[parent] != tar.TypeDir {
			return "", "", phase, errors.New("backup archive entry parent is missing or unsafe")
		}
	}
	if _, exists := seen[header.Name]; exists {
		return "", "", phase, errors.New("backup archive contains a path collision")
	}
	if header.Typeflag == tar.TypeSymlink && !safeSymlink(header.Name, header.Linkname) {
		return "", "", phase, errors.New("backup archive contains an unsafe symbolic link")
	}
	seen[header.Name] = header.Typeflag

	selected := &stats.state
	if volume == "work" {
		selected = &stats.work
	}
	if selected.entries >= maxEntries {
		return "", "", phase, errors.New("backup volume has too many entries")
	}
	if header.Size > maxVolumeBytes-selected.bytes {
		return "", "", phase, errors.New("backup volume content exceeds the format limit")
	}
	selected.entries++
	selected.bytes += header.Size
	return volume, relative, phase, nil
}

func validExtendedHeader(header *tar.Header) bool {
	if len(header.Xattrs) != 0 {
		return false
	}
	for key := range header.PAXRecords {
		switch key {
		case "path", "linkpath", "size":
		default:
			return false
		}
	}
	return true
}

func validArchiveName(name string) bool {
	return name != "" && len(name) <= maxArchivePath && utf8.ValidString(name) &&
		!strings.ContainsRune(name, 0) && !strings.ContainsRune(name, '\\') &&
		!path.IsAbs(name) && path.Clean(name) == name && name != "." &&
		name != metadataName && name != completionName
}

func safeSymlink(name, target string) bool {
	if target == "" || len(target) > maxArchivePath || !utf8.ValidString(target) ||
		strings.ContainsRune(target, 0) || strings.ContainsRune(target, '\\') || path.IsAbs(target) {
		return false
	}
	for _, component := range strings.Split(target, "/") {
		if component == ".." {
			return false
		}
	}
	volume, relative, ok := strings.Cut(name, "/")
	if !ok || relative == "" {
		return false
	}
	resolved := path.Clean(path.Join(path.Dir(relative), target))
	return resolved != ".." && !strings.HasPrefix(resolved, "../") && resolved != "/" && volume != ""
}

func validControlHeader(header *tar.Header, name string) bool {
	return header != nil && header.Name == name && header.Typeflag == tar.TypeReg &&
		header.Mode == 0o600 && header.Size >= 0 && header.Size <= maxControlBytes && header.Linkname == "" &&
		validExtendedHeader(header)
}

func readSmallEntry(reader io.Reader, size int64) ([]byte, error) {
	if size < 0 || size > maxControlBytes {
		return nil, errors.New("invalid control record")
	}
	content := make([]byte, size)
	_, err := io.ReadFull(reader, content)
	return content, err
}

func decodeMetadata(content []byte) (Metadata, error) {
	var document metadataDocument
	if err := decodeStrictJSON(content, &document); err != nil || document.FormatVersion != formatVersion {
		return Metadata{}, errors.New("invalid metadata")
	}
	metadata := Metadata{
		Image: document.Image, ImageID: document.ImageID,
		Profile: document.Profile, Provider: document.Provider,
	}
	if !validMetadata(metadata) {
		return Metadata{}, errors.New("invalid metadata")
	}
	return metadata, nil
}

func validMetadata(metadata Metadata) bool {
	return validMetadataField(metadata.Image) && validMetadataField(metadata.ImageID) &&
		validMetadataField(metadata.Profile) && validMetadataField(metadata.Provider)
}

func validMetadataField(value string) bool {
	if value == "" || len(value) > maxMetadataField || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validateCompletion(content []byte, stats archiveStats, digest []byte) error {
	var document completionDocument
	if err := decodeStrictJSON(content, &document); err != nil || document.FormatVersion != formatVersion ||
		document.StateEntries != stats.state.entries || document.StateBytes != stats.state.bytes ||
		document.WorkEntries != stats.work.entries || document.WorkBytes != stats.work.bytes {
		return errors.New("backup completion record is invalid")
	}
	recorded, err := hex.DecodeString(document.Digest)
	if err != nil || len(recorded) != sha256.Size || subtle.ConstantTimeCompare(recorded, digest) != 1 {
		return errors.New("backup content verification failed")
	}
	return nil
}

func decodeStrictJSON(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func hashEntryHeader(digest hash.Hash, name string, typeflag byte, mode int64, linkname string, size int64) {
	hashString(digest, name)
	_, _ = digest.Write([]byte{typeflag})
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(mode))
	_, _ = digest.Write(number[:])
	hashString(digest, linkname)
	binary.BigEndian.PutUint64(number[:], uint64(size))
	_, _ = digest.Write(number[:])
}

func hashString(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func archiveError(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("backup read cancelled")
	}
	return errors.New("backup archive is invalid or incomplete")
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer *contextWriter) Write(buffer []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	return writer.writer.Write(buffer)
}
