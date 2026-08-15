module github.com/jryannel/sqlb/example/auth-workos

go 1.25.7

replace github.com/jryannel/sqlb => ../../

require (
	github.com/MicahParks/jwkset v0.11.3
	github.com/MicahParks/keyfunc/v3 v3.8.1
	github.com/golang-jwt/jwt/v5 v5.3.1
)

require golang.org/x/time v0.15.0 // indirect
