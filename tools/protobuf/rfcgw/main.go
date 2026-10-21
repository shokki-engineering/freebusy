// Command rfcgw points the generated RFC HTTP gateways at the BSR SDK's gRPC
// packages.
//
// grpc-gateway's standalone mode assumes a service's client and server
// interfaces live in the same Go package as its messages. The BSR SDK for
// buf.build/the-protobuf-project/rfc splits them: messages in
// .../protocolbuffers/go/<path>, gRPC stubs in .../grpc/go/<path>/<pkg>grpc.
// So every standalone gateway names extEventv1.EventsClient where the type is
// eventv1grpc.EventsClient. This rewrites exactly those references -- the
// <Service>Client, <Service>Server and New<Service>Client of each service the
// file registers -- and adds the import, leaving message references alone.
//
// Usage (run by `just gen rfc`, after buf generate):
//
//	go run ./tools/protobuf/rfcgw [dir]
//
// dir defaults to protobuf/generated/go/rfc.
package main

import (
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	messagesModule = "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go"
	grpcModule     = "buf.build/gen/go/the-protobuf-project/rfc/grpc/go"
)

var (
	// sdkImport matches an aliased import of an SDK messages package, e.g.
	// `extEventv1 "buf.build/.../rfc5545/event/v1"`; the alias is
	// grpc-gateway's choice and not always ext-prefixed.
	sdkImport = regexp.MustCompile(`(?m)^\t(\w+) "` + regexp.QuoteMeta(messagesModule) + `/([^"]+)"$`)
	// services are the services a gateway file registers.
	services = regexp.MustCompile(`func Register(\w+)HandlerServer\(`)
)

func main() {
	dir := "protobuf/generated/go/rfc"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	n := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".pb.gw.go") {
			return err
		}
		changed, err := rewrite(path)
		if changed {
			n++
		}
		return err
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "rfcgw:", err)
		os.Exit(1)
	}
	fmt.Printf("rfcgw: pointed %d gateway files at the SDK's gRPC packages\n", n)
}

func rewrite(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	src := string(raw)
	svcs := services.FindAllStringSubmatch(src, -1)
	changed := false
	for _, m := range sdkImport.FindAllStringSubmatch(src, -1) {
		alias, pkgPath := m[1], m[2]
		// The SDK names a package for its last two path elements --
		// .../rfc5545/event/v1 holds eventv1 -- and puts its gRPC stubs in
		// <that>grpc beneath it.
		parts := strings.Split(pkgPath, "/")
		pkgName := parts[len(parts)-2] + parts[len(parts)-1]
		grpcAlias := pkgName + "grpc"
		used := false
		for _, s := range svcs {
			for _, name := range []string{"New" + s[1] + "Client", s[1] + "Client", s[1] + "Server"} {
				re := regexp.MustCompile(`\b` + alias + `\.` + name + `\b`)
				if re.MatchString(src) {
					src = re.ReplaceAllString(src, grpcAlias+"."+name)
					used = true
				}
			}
		}
		if used {
			grpcPath := grpcModule + "/" + pkgPath + "/" + grpcAlias
			src = strings.Replace(src, m[0], m[0]+"\n\t"+grpcAlias+` "`+grpcPath+`"`, 1)
			changed = true
		}
	}
	if !changed {
		return false, nil // already rewritten, or nothing to point elsewhere
	}
	out, err := format.Source([]byte(src))
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, os.WriteFile(path, out, 0o644)
}
