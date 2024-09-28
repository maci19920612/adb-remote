package shared

/**
Planned behaviour:
Client A:
- (Client -> Server) Connects to a server (protocolVersion: int)
- 	(Server -> Client) Client identifier (clientIdentifier: string)
-   (Server -> Client) Protocol rejection (errorCode: int, errorMessage: string)
- (Client -> Server) Create room (Unit)
-   (Server -> Client) Room created (roomIdentifier: string)
-   (Server -> Client) Server full rejection (errorCode: int, errorMessage: string)
- (Client -> Server) Connect to room (roomIdentifier string)
-   (Server -> Client) Connection accepted
-   (Server -> Client) Room full error (errorCode: int, errorMessage: string)
-   (Server -> Client) Room does not exists (errorCode: int, errorMessage: string)
- (Server -> Client[Room owner]) Room connection requested (clientIdentifier: string)
-   (Client -> Server) Accept
-   (Client -> Server) Reject
- (Client[Guest] -> Server) - AdbTransport
- (Client[Owner] -> Server) - AdbTransport
*/
