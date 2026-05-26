# Release minification is currently off. If you enable it, keep the classes that
# are instantiated/invoked reflectively or over AIDL/Shizuku:

# Shizuku instantiates the UserService by name in the shell process.
-keep class com.bgnconnect.app.shizuku.ClipboardUserService { *; }

# AIDL stubs and the bundled hidden listener.
-keep class com.bgnconnect.app.shizuku.** { *; }
-keep class android.content.IOnPrimaryClipChangedListener { *; }
-keep class android.content.IOnPrimaryClipChangedListener$Stub { *; }

# Shizuku API.
-keep class rikka.shizuku.** { *; }
-dontwarn rikka.shizuku.**
