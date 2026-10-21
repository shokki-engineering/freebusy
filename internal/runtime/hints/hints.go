// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hints turns the API's generic errors into ones a developer can act
// on, after the handler has run.
//
// The generated repositories report a missing, duplicate or concurrently
// changed record with a bare sentinel -- "not found", "already exists",
// "conflict" -- because they cannot know what the caller was doing. The
// request can: it names the resource. So a bare error is rewritten to say
// which resource, of what kind, and what to do next -- the List call that
// shows what does exist, the Get that reads what is in the way -- with the
// gRPC method and its REST path, both read from the protos, so the hint is
// right on whichever surface the caller used.
//
// An Internal error is a fault on our side, and its text -- a SQL error, a
// stack of wrapping -- helps nobody calling the API and says more about the
// database than it should. It is replaced by an apology that carries the
// trace id, which is what a developer can report and an operator can look
// up; the original is in that trace and the log.
package hints

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Interceptor rewrites generic errors into actionable ones.
func Interceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}
		m, _ := req.(proto.Message)
		return resp, Rewrite(ctx, info.FullMethod, m, err)
	}
}

// Rewrite returns err with a hint when it is one of the generic errors and
// the request says enough to give one, and err unchanged otherwise.
func Rewrite(ctx context.Context, fullMethod string, req proto.Message, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	if st.Code() == codes.Internal || st.Code() == codes.Unknown {
		return internal(ctx, fullMethod)
	}
	if req == nil {
		return err
	}
	// A service that knew more said so with its own reason; only what the
	// repositories reported generically is rewritten.
	if !defaultReason(st) {
		return err
	}
	method := methodName(fullMethod)
	switch st.Code() {
	case codes.NotFound:
		// Only a bare "not found": a message that names something was
		// written by someone who knew what was missing, which need not be
		// the resource the request names.
		if generic(st.Message()) != "" {
			if name := named(req); name != "" {
				return notFound(st, name)
			}
		}
	case codes.AlreadyExists:
		if strings.HasPrefix(method, "Undelete") {
			if name := named(req); name != "" {
				return notDeleted(st, name)
			}
		}
		if name := created(req); name != "" {
			return alreadyExists(st, name)
		}
	case codes.Aborted:
		if name := updated(req); name != "" {
			return stale(st, name)
		}
	}
	return err
}

// defaultReason reports a status whose ErrorInfo, if any, carries one of the
// generic reasons rpc.ToStatusErr gives an error nobody described further.
func defaultReason(st *status.Status) bool {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			switch info.GetReason() {
			case "", "NOT_FOUND", "ALREADY_EXISTS", "CONCURRENT_MODIFICATION":
				return true
			}
			return false
		}
	}
	return true
}

func notDeleted(st *status.Status, name string) error {
	r := lookup(name)
	msg := fmt.Sprintf("%s %q is not deleted, so there is nothing to restore", r.label(), name)
	if g := r.getCall(name); g != "" {
		msg += "; " + g + " reads it as it is"
	}
	return with(st, msg+".", &errdetails.ResourceInfo{ResourceType: r.Type, ResourceName: name, Description: "not deleted"})
}

// generic is msg when it is a bare sentinel with nothing said about what.
func generic(msg string) string {
	switch m := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(msg)), "."); m {
	case "not found", "already exists", "conflict":
		return m
	}
	return ""
}

func notFound(st *status.Status, name string) error {
	// "organizationalUnits/x:checkAvailability" sent as GET reaches the Get
	// of organizationalUnits/x, verb and all: the caller meant the custom
	// method, which is a POST.
	if base, verb, ok := strings.Cut(name, ":"); ok && verb != "" && !strings.Contains(verb, "/") {
		r := lookup(base)
		return with(st, fmt.Sprintf("%q is not a %s name: :%s is a custom method, and custom methods are called with POST, not GET.",
			name, strings.ToLower(r.label()), verb), &errdetails.ResourceInfo{ResourceType: r.Type, ResourceName: base, Description: "custom method called with GET"})
	}
	r := lookup(name)
	msg := fmt.Sprintf("%s %q does not exist. Check the name", r.label(), name)
	if l := r.listCall(parentOf(name)); l != "" {
		msg += ", or list what does exist with " + l
	}
	return with(st, msg+".", &errdetails.ResourceInfo{ResourceType: r.Type, ResourceName: name, Description: "does not exist"})
}

func alreadyExists(st *status.Status, name string) error {
	r := lookup(name)
	msg := fmt.Sprintf("%s %q already exists. Choose another id, or leave the id empty to have one assigned", r.label(), name)
	if g := r.getCall(name); g != "" {
		msg += "; " + g + " reads the existing one"
	}
	return with(st, msg+".", &errdetails.ResourceInfo{ResourceType: r.Type, ResourceName: name, Description: "already exists"})
}

func stale(st *status.Status, name string) error {
	r := lookup(name)
	msg := fmt.Sprintf("%s %q changed after you read it: the etag you sent is no longer current", r.label(), name)
	if g := r.getCall(name); g != "" {
		msg += ". Read it again with " + g + ", reapply your change and retry with the new etag"
	} else {
		msg += ". Read it again, reapply your change and retry with the new etag"
	}
	return with(st, msg+".", &errdetails.ResourceInfo{ResourceType: r.Type, ResourceName: name, Description: "etag is stale"})
}

func internal(ctx context.Context, fullMethod string) error {
	msg := "freebusy failed unexpectedly"
	if m := methodName(fullMethod); m != "" {
		msg += " while handling " + m
	}
	msg += ". This is a fault on our side, not in your request: retry it, and if it keeps failing, report"
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		msg += " trace " + sc.TraceID().String()
	} else {
		msg += " the time and the call"
	}
	return status.Error(codes.Internal, msg+".")
}

// with replaces st's message, keeping its details and adding info.
func with(st *status.Status, msg string, info *errdetails.ResourceInfo) error {
	p := st.Proto()
	p.Message = msg
	out := status.FromProto(p)
	if detailed, err := out.WithDetails(info); err == nil {
		return detailed.Err()
	}
	return out.Err()
}

// named is the resource a request is about: its name, or the name of the
// resource it carries (an update sends the resource itself).
func named(req proto.Message) string {
	r := req.ProtoReflect()
	if v := stringField(r, "name"); v != "" {
		return v
	}
	return updated(req)
}

// updated is the name of the resource message a request carries.
func updated(req proto.Message) string {
	r := req.ProtoReflect()
	if v := stringField(r, "name"); v != "" {
		return v
	}
	var out string
	r.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() && isResource(fd.Message()) {
			out = stringField(v.Message(), "name")
			return out == ""
		}
		return true
	})
	return out
}

// created is the name a create asked for: the resource's own name if it set
// one, else parent plus the caller-chosen id (AIP-133's <resource>_id).
func created(req proto.Message) string {
	r := req.ProtoReflect()
	if n := updated(req); n != "" {
		return n
	}
	parent := stringField(r, "parent")
	fields := r.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.MessageKind || !isResource(fd.Message()) {
			continue
		}
		id := stringField(r, string(fd.Name())+"_id")
		if id == "" {
			return ""
		}
		res := resourceOf(fd.Message())
		segs := strings.Split(res.GetPattern()[0], "/")
		if len(segs) < 2 {
			return ""
		}
		name := segs[len(segs)-2] + "/" + id
		if parent != "" {
			name = parent + "/" + name
		}
		return name
	}
	return ""
}

func stringField(r protoreflect.Message, name string) string {
	fd := r.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil || fd.Kind() != protoreflect.StringKind || fd.IsList() {
		return ""
	}
	return r.Get(fd).String()
}

func isResource(md protoreflect.MessageDescriptor) bool { return resourceOf(md) != nil }

func resourceOf(md protoreflect.MessageDescriptor) *annotations.ResourceDescriptor {
	res, _ := proto.GetExtension(md.Options(), annotations.E_Resource).(*annotations.ResourceDescriptor)
	if res == nil || len(res.GetPattern()) == 0 {
		return nil
	}
	return res
}

// parentOf is the parent of a resource name, "" for a top-level one.
func parentOf(name string) string {
	segs := strings.Split(name, "/")
	if len(segs) < 4 {
		return ""
	}
	return strings.Join(segs[:len(segs)-2], "/")
}

func methodName(fullMethod string) string {
	if i := strings.LastIndex(fullMethod, "/"); i >= 0 {
		return fullMethod[i+1:]
	}
	return ""
}

// kind is what the protos say about one resource.
type kind struct {
	Type     string // "the-protobuf-project.org/Organization"
	Singular string // "organization"
	Plural   string // "organizations"
	pattern  []string
	list     *call // List<Plural>
	get      *call // Get<Singular>
}

// call is an RPC and its REST binding.
type call struct {
	method string // "ListOrganizations"
	verb   string // "GET"
	path   string // "/v1/{parent=organizationalUnits/*}/reservations"
}

// label is the kind in words: "organizational unit".
func (k kind) label() string {
	if k.Singular == "" {
		return "resource"
	}
	var b strings.Builder
	for i, r := range k.Singular {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	s := b.String()
	return strings.ToUpper(s[:1]) + s[1:]
}

func (k kind) listCall(parent string) string {
	return k.list.render(map[string]string{"parent": parent})
}

func (k kind) getCall(name string) string {
	return k.get.render(map[string]string{"name": name})
}

var pathVar = regexp.MustCompile(`\{([a-z_.]+)(=[^}]*)?\}`)

// render is "ListReservations (GET /v1/organizationalUnits/x/reservations)",
// filling the path's variables from vars.
func (c *call) render(vars map[string]string) string {
	if c == nil {
		return ""
	}
	if c.path == "" {
		return c.method
	}
	path := pathVar.ReplaceAllStringFunc(c.path, func(m string) string {
		v := pathVar.FindStringSubmatch(m)[1]
		if val := vars[v]; val != "" {
			return val
		}
		return m
	})
	return fmt.Sprintf("%s (%s %s)", c.method, c.verb, path)
}

// lookup finds the kind whose pattern a name has.
func lookup(name string) kind {
	segs := strings.Split(name, "/")
	for _, k := range kinds() {
		if len(k.pattern) != len(segs) {
			continue
		}
		match := true
		for i, p := range k.pattern {
			if !strings.HasPrefix(p, "{") && p != segs[i] {
				match = false
				break
			}
		}
		if match {
			return k
		}
	}
	return kind{}
}

// TypeOf is the resource type a name has, as its proto declares it
// ("the-protobuf-project.org/Organization"), or "" when none matches.
func TypeOf(name string) string { return lookup(name).Type }

// Call renders an RPC for a message: its name and REST binding, with the
// path's variables filled from vars -- Call("UndeleteOrganization",
// map[string]string{"name": "organizations/acme"}) is
// "UndeleteOrganization (POST /v1/organizations/acme:undelete)". An unknown
// method renders as its name alone, so a hint never breaks over a rename.
func Call(method string, vars map[string]string) string {
	kinds()
	c := allCalls[method]
	if c == nil {
		return method
	}
	return c.render(vars)
}

var (
	kindsOnce sync.Once
	allKinds  []kind
	allCalls  map[string]*call
)

// kinds reads every registered resource and its List and Get methods.
func kinds() []kind {
	kindsOnce.Do(func() {
		calls := map[string]*call{}
		var resources []*annotations.ResourceDescriptor
		protoregistry.GlobalFiles.RangeFiles(func(f protoreflect.FileDescriptor) bool {
			var walk func(protoreflect.MessageDescriptors)
			walk = func(ms protoreflect.MessageDescriptors) {
				for i := 0; i < ms.Len(); i++ {
					if res := resourceOf(ms.Get(i)); res != nil {
						resources = append(resources, res)
					}
					walk(ms.Get(i).Messages())
				}
			}
			walk(f.Messages())
			for i := 0; i < f.Services().Len(); i++ {
				ms := f.Services().Get(i).Methods()
				for j := 0; j < ms.Len(); j++ {
					m := ms.Get(j)
					c := &call{method: string(m.Name())}
					if rule, _ := proto.GetExtension(m.Options(), annotations.E_Http).(*annotations.HttpRule); rule != nil {
						switch {
						case rule.GetGet() != "":
							c.verb, c.path = "GET", rule.GetGet()
						case rule.GetPost() != "":
							c.verb, c.path = "POST", rule.GetPost()
						case rule.GetPatch() != "":
							c.verb, c.path = "PATCH", rule.GetPatch()
						case rule.GetDelete() != "":
							c.verb, c.path = "DELETE", rule.GetDelete()
						}
					}
					calls[c.method] = c
				}
			}
			return true
		})
		allCalls = calls
		for _, res := range resources {
			k := kind{Type: res.GetType(), Singular: res.GetSingular(), Plural: res.GetPlural(), pattern: strings.Split(res.GetPattern()[0], "/")}
			if k.Plural != "" {
				k.list = calls["List"+upperFirst(k.Plural)]
			}
			if k.Singular != "" {
				k.get = calls["Get"+upperFirst(k.Singular)]
			}
			allKinds = append(allKinds, k)
		}
	})
	return allKinds
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
