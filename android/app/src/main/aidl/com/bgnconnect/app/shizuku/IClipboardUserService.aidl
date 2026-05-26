package com.bgnconnect.app.shizuku;

import android.content.ClipData;
import com.bgnconnect.app.shizuku.IClipboardCallback;

// Implemented by the process Shizuku starts as the shell user (uid 2000).
// `destroy` uses the transaction id Shizuku reserves for tearing down a UserService.
interface IClipboardUserService {
    void destroy() = 16777114;
    void startWatching(IClipboardCallback callback) = 1;
    void stopWatching() = 2;
    void setClipboard(String text) = 3;
    // ClipData (a content:// uri the app already owns) is small — passes over binder fine.
    void setClipboardImage(in ClipData clip) = 4;
}
