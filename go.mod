module tunneltug

go 1.25.10

require (
	github.com/0TrustCloud/mesh_client v0.0.0
	github.com/0TrustCloud/secure_data_format v1.0.0
	github.com/0TrustCloud/secure_dns v1.0.1
	github.com/0TrustCloud/secure_registrar v1.0.0
	github.com/0TrustCloud/ultimate_db v1.3.5
	github.com/hashicorp/yamux v0.1.2
	github.com/quic-go/quic-go v0.59.1
	golang.org/x/crypto v0.53.0
	gopkg.in/yaml.v3 v3.0.1
	k8s.io/api v0.32.3
	k8s.io/apimachinery v0.32.3
	k8s.io/client-go v0.32.3
)

replace github.com/0TrustCloud/mesh_client => ../0TrustCloud/modules/mesh_client

replace github.com/0TrustCloud/secure_ssh => ../0TrustCloud/modules/secure_ssh

replace github.com/0TrustCloud/secure_k8s => ../0TrustCloud/modules/secure_k8s

replace github.com/0TrustCloud/secure_network => ../0TrustCloud/modules/secure_network

replace github.com/0TrustCloud/secure_policy => ../0TrustCloud/modules/secure_policy

replace github.com/0TrustCloud/secure_data_format => ../0TrustCloud/modules/secure_data_format

replace github.com/0TrustCloud/secure_dns => ../0TrustCloud/modules/secure_dns

replace github.com/0TrustCloud/secure_registrar => ../0TrustCloud/modules/secure_registrar

replace github.com/0TrustCloud/ultimate_db => ../0TrustCloud/modules/ultimate_db

replace github.com/0TrustCloud/logger => ../0TrustCloud/modules/logger

replace github.com/0TrustCloud/auth_provider => ../0TrustCloud/modules/auth_provider

replace github.com/0TrustCloud/guikit => ../0TrustCloud/modules/guikit

replace github.com/0TrustCloud/samln => ../0TrustCloud/modules/samln

require (
	github.com/0TrustCloud/auth_provider v1.0.1 // indirect
	github.com/0TrustCloud/guikit v1.1.3-0.20260530040829-bb3a7bb56546 // indirect
	github.com/0TrustCloud/logger v1.0.3 // indirect
	github.com/0TrustCloud/samln v0.0.0 // indirect
	github.com/0TrustCloud/secure_k8s v0.0.0 // indirect
	github.com/0TrustCloud/secure_network v1.1.4 // indirect
	github.com/0TrustCloud/secure_policy v1.0.6 // indirect
	github.com/0TrustCloud/secure_ssh v0.0.0 // indirect
	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emicklei/go-restful/v3 v3.11.0 // indirect
	github.com/flynn/noise v1.1.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.2 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-openapi/jsonpointer v0.21.0 // indirect
	github.com/go-openapi/jsonreference v0.20.2 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/webauthn v0.17.4 // indirect
	github.com/go-webauthn/x v0.2.6 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/gnostic-models v0.6.8 // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pquerna/otp v1.5.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/oauth2 v0.23.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/term v0.44.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/time v0.7.0 // indirect
	google.golang.org/protobuf v1.35.1 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.12.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/square/go-jose.v2 v2.6.0 // indirect
	k8s.io/klog/v2 v2.130.1 // indirect
	k8s.io/kube-openapi v0.0.0-20241105132330-32ad38e42d3f // indirect
	k8s.io/utils v0.0.0-20241104100929-3ea5e8cea738 // indirect
	sigs.k8s.io/json v0.0.0-20241010143419-9aa6b5e7a4b3 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.4.2 // indirect
	sigs.k8s.io/yaml v1.4.0 // indirect
)
