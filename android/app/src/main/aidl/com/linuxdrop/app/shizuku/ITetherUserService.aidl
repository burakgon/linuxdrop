package com.linuxdrop.app.shizuku;

// Implemented by the process Shizuku starts as the shell user (uid 2000).
// `destroy` uses the transaction id Shizuku reserves for tearing down a UserService.
interface ITetherUserService {
    void destroy() = 16777114;
    // Returns a TetherResult code (0 = OK). Pins a fixed SoftAp config then starts Wi-Fi tethering.
    int enableHotspot(String ssid, String passphrase) = 1;
    int disableHotspot() = 2;
}
