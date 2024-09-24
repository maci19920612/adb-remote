package shared

import (
	"errors"
	"fmt"
)

const CommandSYNC uint32 = 0x434e5953
const CommandCNXN uint32 = 0x4e584e43
const CommandOPEN uint32 = 0x4e45504f
const CommandOKAY uint32 = 0x59414b4f
const CommandCLSE uint32 = 0x45534c43
const CommandWRTE uint32 = 0x45545257

type rawAdbMessage struct {
	Command     uint
	Arg0        uint
	Arg1        uint
	Data_length uint
	Magic       uint
}

const headerLength = 6 * 4

// Little endian
func ParseHeader(rawPackage *[]byte, target *AdbMessage) error {
	if len(*rawPackage) < headerLength {
		return errors.New(fmt.Sprintf("Invalid raw packge: Too short, required min length is: %d", headerLength))
	}

	return nil

}

// struct message {
//     unsigned command;       /* command identifier constant      */
//     unsigned arg0;          /* first argument                   */
//     unsigned arg1;          /* second argument                  */
//     unsigned data_length;   /* length of payload (0 is allowed) */
//     unsigned data_crc32;    /* crc32 of data payload            */
//     unsigned magic;         /* command ^ 0xffffffff             */
// };
