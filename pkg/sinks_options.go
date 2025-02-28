package mediasink

// type PacketisationMode uint8
//
// const (
// 	PacketisationMode0 PacketisationMode = 0
// 	PacketisationMode1 PacketisationMode = 1
// 	PacketisationMode2 PacketisationMode = 2
// )
//
// type ProfileLevel string
//
// const (
// 	ProfileLevelBaseline21 ProfileLevel = "420015" // Level 2.1 (480p)
// 	ProfileLevelBaseline31 ProfileLevel = "42001f" // Level 3.1 (720p)
// 	ProfileLevelBaseline41 ProfileLevel = "420029" // Level 4.1 (1080p)
// 	ProfileLevelBaseline42 ProfileLevel = "42002a" // Level 4.2 (2K)
//
// 	ProfileLevelMain21 ProfileLevel = "4D0015" // Level 2.1
// 	ProfileLevelMain31 ProfileLevel = "4D001f" // Level 3.1
// 	ProfileLevelMain41 ProfileLevel = "4D0029" // Level 4.1
// 	ProfileLevelMain42 ProfileLevel = "4D002a" // Level 4.2
//
// 	ProfileLevelHigh21 ProfileLevel = "640015" // Level 2.1
// 	ProfileLevelHigh31 ProfileLevel = "64001f" // Level 3.1
// 	ProfileLevelHigh41 ProfileLevel = "640029" // Level 4.1
// 	ProfileLevelHigh42 ProfileLevel = "64002a" // Level 4.2
// )

type SinksOptions = func(*Sinks) error
