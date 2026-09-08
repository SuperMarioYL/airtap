package main
import (
 "fmt"
 "github.com/SuperMarioYL/airtap/internal/manifest"
 "github.com/SuperMarioYL/airtap/internal/egress"
)
func main() {
 m, err := manifest.Load("examples/airtap.yaml"); if err != nil { panic(err) }
 fmt.Printf("manifest: valid; model=%s; tools=%v\n",m.Model.Name,m.Agent.Tools)
 p:=egress.NewProxy(m.Egress.Allow,nil)
 for _,target:=range []string{"127.0.0.1:8000","example.com:443"} { fmt.Printf("%s allowed=%v\n",target,p.Allowed(target)) }
}
