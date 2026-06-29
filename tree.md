列出資料夾 PATH
磁碟區序號為 6659-7B3E
C:.
|   .gitignore
|   Power IoT System - 專案需求與規格書 (PRD)v3.2.md
|   Power IoT System - 軟體開發生命週期 (SDLC)v3.2.md
|   README.md
|   tree.md
|   後端開發草稿.md
|   
+---backend
|   |   go.mod
|   |   go.sum
|   |   
|   +---cmd
|   |   \---server
|   |           main.go
|   |           
|   +---config
|   |       config.yaml
|   |       
|   \---internal
|       +---adapters
|       |   +---http
|       |   \---persistence
|       +---api
|       |   +---handlers
|       |   \---middleware
|       +---application
|       +---core
|       |   +---identity
|       |   +---iot
|       |   \---telemetry
|       +---data
|       |   +---migrations
|       |   \---models
|       |           schema.go
|       |           
|       +---domain
|       +---infrastructure
|       +---ports
|       \---utils
+---infrastructure
|   |   docker-compose.yml
|   |   
|   \---mosquitto
|       +---config
|       |       mosquitto.conf
|       |       
|       +---data
|       \---log
\---mobile
    |   .flutter-plugins-dependencies
    |   .gitignore
    |   .metadata
    |   analysis_options.yaml
    |   mobile.iml
    |   pubspec.lock
    |   pubspec.yaml
    |   README.md
    |   
    +---.dart_tool
    |   |   package_config.json
    |   |   package_graph.json
    |   |   version
    |   |   
    |   +---chrome-device
    |   |   \---Default
    |   |       |   Account Web Data
    |   |       |   Account Web Data-journal
    |   |       |   Affiliation Database
    |   |       |   Affiliation Database-journal
    |   |       |   BookmarkMergedSurfaceOrdering
    |   |       |   BrowsingTopicsSiteData
    |   |       |   BrowsingTopicsSiteData-journal
    |   |       |   BrowsingTopicsState
    |   |       |   DIPS
    |   |       |   DIPS-wal
    |   |       |   Favicons
    |   |       |   Favicons-journal
    |   |       |   heavy_ad_intervention_opt_out.db
    |   |       |   heavy_ad_intervention_opt_out.db-journal
    |   |       |   History
    |   |       |   History-journal
    |   |       |   LOCK
    |   |       |   LOG
    |   |       |   Login Data
    |   |       |   Login Data For Account
    |   |       |   Login Data For Account-journal
    |   |       |   Login Data-journal
    |   |       |   Network Action Predictor
    |   |       |   Network Action Predictor-journal
    |   |       |   Preferences
    |   |       |   PreferredApps
    |   |       |   README
    |   |       |   Secure Preferences
    |   |       |   ServerCertificate
    |   |       |   ServerCertificate-journal
    |   |       |   SharedStorage
    |   |       |   SharedStorage-wal
    |   |       |   Shortcuts
    |   |       |   Shortcuts-journal
    |   |       |   Top Sites
    |   |       |   Top Sites-journal
    |   |       |   trusted_vault.pb
    |   |       |   Web Data
    |   |       |   Web Data-journal
    |   |       |   
    |   |       +---AutofillStrikeDatabase
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---blob_storage
    |   |       |   \---40242437-2e41-456b-99de-10f5cbe839fd
    |   |       +---BudgetDatabase
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---chrome_cart_db
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---ClientCertificates
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---commerce_subscription_db
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---discounts_db
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---discount_infos_db
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---Download Service
    |   |       |   +---EntryDB
    |   |       |   |       LOCK
    |   |       |   |       LOG
    |   |       |   |       
    |   |       |   \---Files
    |   |       +---Extension Rules
    |   |       |       000003.log
    |   |       |       CURRENT
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       MANIFEST-000001
    |   |       |       
    |   |       +---Extension Scripts
    |   |       |       000003.log
    |   |       |       CURRENT
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       MANIFEST-000001
    |   |       |       
    |   |       +---Extension State
    |   |       |       000003.log
    |   |       |       CURRENT
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       MANIFEST-000001
    |   |       |       
    |   |       +---Feature Engagement Tracker
    |   |       |   +---AvailabilityDB
    |   |       |   |       LOCK
    |   |       |   |       LOG
    |   |       |   |       
    |   |       |   \---EventDB
    |   |       |           LOCK
    |   |       |           LOG
    |   |       |           
    |   |       +---GCM Store
    |   |       |   |   000003.log
    |   |       |   |   CURRENT
    |   |       |   |   LOCK
    |   |       |   |   LOG
    |   |       |   |   MANIFEST-000001
    |   |       |   |   
    |   |       |   \---Encryption
    |   |       |           000003.log
    |   |       |           CURRENT
    |   |       |           LOCK
    |   |       |           LOG
    |   |       |           MANIFEST-000001
    |   |       |           
    |   |       +---Local Storage
    |   |       |   \---leveldb
    |   |       |           000003.log
    |   |       |           CURRENT
    |   |       |           LOCK
    |   |       |           LOG
    |   |       |           MANIFEST-000001
    |   |       |           
    |   |       +---Network
    |   |       |       Cookies
    |   |       |       Cookies-journal
    |   |       |       Network Persistent State
    |   |       |       NetworkDataMigrated
    |   |       |       Reporting and NEL
    |   |       |       Reporting and NEL-journal
    |   |       |       Trust Tokens
    |   |       |       Trust Tokens-journal
    |   |       |       
    |   |       +---optimization_guide_hint_cache_store
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---parcel_tracking_db
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---PersistentOriginTrials
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       
    |   |       +---Safe Browsing Network
    |   |       |       NetworkDataMigrated
    |   |       |       Safe Browsing Cookies
    |   |       |       Safe Browsing Cookies-journal
    |   |       |       
    |   |       +---Segmentation Platform
    |   |       |   +---SegmentInfoDB
    |   |       |   |       LOCK
    |   |       |   |       LOG
    |   |       |   |       
    |   |       |   +---SignalDB
    |   |       |   |       LOCK
    |   |       |   |       LOG
    |   |       |   |       
    |   |       |   \---SignalStorageConfigDB
    |   |       |           LOCK
    |   |       |           LOG
    |   |       |           
    |   |       +---Service Worker
    |   |       |   +---Database
    |   |       |   |       000003.log
    |   |       |   |       CURRENT
    |   |       |   |       LOCK
    |   |       |   |       LOG
    |   |       |   |       MANIFEST-000001
    |   |       |   |       
    |   |       |   \---ScriptCache
    |   |       |       |   2cc80dabc69f58b6_0
    |   |       |       |   4cb013792b196a35_0
    |   |       |       |   4cb013792b196a35_1
    |   |       |       |   index
    |   |       |       |   
    |   |       |       \---index-dir
    |   |       |               the-real-index
    |   |       |               
    |   |       +---Session Storage
    |   |       |       000003.log
    |   |       |       CURRENT
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       MANIFEST-000001
    |   |       |       
    |   |       +---Sessions
    |   |       |       Session_13408739121721991
    |   |       |       
    |   |       +---Shared Dictionary
    |   |       |   |   db
    |   |       |   |   db-journal
    |   |       |   |   
    |   |       |   \---cache
    |   |       |       |   index
    |   |       |       |   
    |   |       |       \---index-dir
    |   |       |               the-real-index
    |   |       |               
    |   |       +---shared_proto_db
    |   |       |   |   000003.log
    |   |       |   |   CURRENT
    |   |       |   |   LOCK
    |   |       |   |   LOG
    |   |       |   |   MANIFEST-000001
    |   |       |   |   
    |   |       |   \---metadata
    |   |       |           000003.log
    |   |       |           CURRENT
    |   |       |           LOCK
    |   |       |           LOG
    |   |       |           MANIFEST-000001
    |   |       |           
    |   |       +---Site Characteristics Database
    |   |       |       000003.log
    |   |       |       CURRENT
    |   |       |       LOCK
    |   |       |       LOG
    |   |       |       MANIFEST-000001
    |   |       |       
    |   |       +---Sync Data
    |   |       |   \---LevelDB
    |   |       |           000003.log
    |   |       |           CURRENT
    |   |       |           LOCK
    |   |       |           LOG
    |   |       |           MANIFEST-000001
    |   |       |           
    |   |       \---WebStorage
    |   |               QuotaManager
    |   |               QuotaManager-journal
    |   |               
    |   +---dartpad
    |   |       web_plugin_registrant.dart
    |   |       
    |   +---extension_discovery
    |   |       vs_code.json
    |   |       
    |   +---flutter_build
    |   |   |   dart_plugin_registrant.dart
    |   |   |   
    |   |   \---cd2904f65e98a7f0f0f05e2ac447f337
    |   |           .filecache
    |   |           app.dill
    |   |           dart_build.d
    |   |           dart_build.stamp
    |   |           dart_build_result.json
    |   |           debug_bundle_windows-x64_assets.stamp
    |   |           flutter_assets.d
    |   |           gen_dart_plugin_registrant.stamp
    |   |           gen_localizations.stamp
    |   |           install_code_assets.d
    |   |           install_code_assets.stamp
    |   |           kernel_snapshot_program.d
    |   |           kernel_snapshot_program.stamp
    |   |           native_assets.json
    |   |           outputs.json
    |   |           unpack_windows.stamp
    |   |           windows_engine_sources.d
    |   |           
    |   \---widget_preview_scaffold
    |       |   .gitignore
    |       |   analysis_options.yaml
    |       |   flutter_01.log
    |       |   preview_manifest.json
    |       |   pubspec.lock
    |       |   pubspec.yaml
    |       |   README.md
    |       |   widget_preview_scaffold.iml
    |       |   
    |       +---.dart_tool
    |       |       package_config.json
    |       |       package_graph.json
    |       |       version
    |       |       
    |       +---.idea
    |       |   |   modules.xml
    |       |   |   workspace.xml
    |       |   |   
    |       |   +---libraries
    |       |   |       Dart_SDK.xml
    |       |   |       KotlinJavaRuntime.xml
    |       |   |       
    |       |   \---runConfigurations
    |       |           main_dart.xml
    |       |           
    |       +---build
    |       |   |   .last_build_id
    |       |   |   9d2de1c78311b128d07c8857ed340e04.cache.dill.track.dill
    |       |   |   
    |       |   +---a823f93287d52f524eafcde7d45ab5ae
    |       |   |       .filecache
    |       |   |       dart_build.d
    |       |   |       dart_build.stamp
    |       |   |       dart_build_result.json
    |       |   |       gen_dart_plugin_registrant.stamp
    |       |   |       gen_localizations.stamp
    |       |   |       outputs.json
    |       |   |       _composite.stamp
    |       |   |       
    |       |   +---flutter_assets
    |       |   |   |   AssetManifest.bin
    |       |   |   |   AssetManifest.bin.json
    |       |   |   |   FontManifest.json
    |       |   |   |   NOTICES
    |       |   |   |   
    |       |   |   +---fonts
    |       |   |   |       MaterialIcons-Regular.otf
    |       |   |   |       
    |       |   |   +---packages
    |       |   |   |   \---cupertino_icons
    |       |   |   |       \---assets
    |       |   |   |               CupertinoIcons.ttf
    |       |   |   |               
    |       |   |   \---shaders
    |       |   |           ink_sparkle.frag
    |       |   |           stretch_effect.frag
    |       |   |           
    |       |   \---native_assets
    |       |       \---web
    |       +---lib
    |       |   |   main.dart
    |       |   |   
    |       |   \---src
    |       |       |   controls.dart
    |       |       |   generated_preview.dart
    |       |       |   utils.dart
    |       |       |   widget_preview.dart
    |       |       |   widget_preview_inspector_service.dart
    |       |       |   widget_preview_rendering.dart
    |       |       |   widget_preview_scaffold_controller.dart
    |       |       |   
    |       |       +---dtd
    |       |       |       dtd_services.dart
    |       |       |       editor_service.dart
    |       |       |       utils.dart
    |       |       |       
    |       |       +---theme
    |       |       |       ide_theme.dart
    |       |       |       theme.dart
    |       |       |       _ide_theme_desktop.dart
    |       |       |       _ide_theme_web.dart
    |       |       |       
    |       |       \---utils
    |       |           |   color_utils.dart
    |       |           |   
    |       |           \---url
    |       |                   url.dart
    |       |                   _url_stub.dart
    |       |                   _url_web.dart
    |       |                   
    |       \---web
    |           |   favicon.png
    |           |   index.html
    |           |   manifest.json
    |           |   
    |           \---icons
    |                   Icon-192.png
    |                   Icon-512.png
    |                   Icon-maskable-192.png
    |                   Icon-maskable-512.png
    |                   
    +---.idea
    |   |   modules.xml
    |   |   workspace.xml
    |   |   
    |   +---libraries
    |   |       Dart_SDK.xml
    |   |       KotlinJavaRuntime.xml
    |   |       
    |   \---runConfigurations
    |           main_dart.xml
    |           
    +---android
    |   |   .gitignore
    |   |   build.gradle.kts
    |   |   gradle.properties
    |   |   gradlew
    |   |   gradlew.bat
    |   |   local.properties
    |   |   mobile_android.iml
    |   |   settings.gradle.kts
    |   |   
    |   +---app
    |   |   |   build.gradle.kts
    |   |   |   
    |   |   \---src
    |   |       +---debug
    |   |       |       AndroidManifest.xml
    |   |       |       
    |   |       +---main
    |   |       |   |   AndroidManifest.xml
    |   |       |   |   
    |   |       |   +---java
    |   |       |   |   \---io
    |   |       |   |       \---flutter
    |   |       |   |           \---plugins
    |   |       |   |                   GeneratedPluginRegistrant.java
    |   |       |   |                   
    |   |       |   +---kotlin
    |   |       |   |   \---com
    |   |       |   |       \---poweriot
    |   |       |   |           \---mobile
    |   |       |   |                   MainActivity.kt
    |   |       |   |                   
    |   |       |   \---res
    |   |       |       +---drawable
    |   |       |       |       launch_background.xml
    |   |       |       |       
    |   |       |       +---drawable-v21
    |   |       |       |       launch_background.xml
    |   |       |       |       
    |   |       |       +---mipmap-hdpi
    |   |       |       |       ic_launcher.png
    |   |       |       |       
    |   |       |       +---mipmap-mdpi
    |   |       |       |       ic_launcher.png
    |   |       |       |       
    |   |       |       +---mipmap-xhdpi
    |   |       |       |       ic_launcher.png
    |   |       |       |       
    |   |       |       +---mipmap-xxhdpi
    |   |       |       |       ic_launcher.png
    |   |       |       |       
    |   |       |       +---mipmap-xxxhdpi
    |   |       |       |       ic_launcher.png
    |   |       |       |       
    |   |       |       +---values
    |   |       |       |       styles.xml
    |   |       |       |       
    |   |       |       \---values-night
    |   |       |               styles.xml
    |   |       |               
    |   |       \---profile
    |   |               AndroidManifest.xml
    |   |               
    |   \---gradle
    |       \---wrapper
    |               gradle-wrapper.jar
    |               gradle-wrapper.properties
    |               
    +---assets
    |   +---icons
    |   \---images
    +---build
    |   |   .last_build_id
    |   |   2620954bb9b03a3ad628b6c1704e3af2.cache.dill.track.dill
    |   |   
    |   +---07b6527eacde4c523f5381d0f3814aa6
    |   |       .filecache
    |   |       dart_build.d
    |   |       dart_build.stamp
    |   |       dart_build_result.json
    |   |       gen_dart_plugin_registrant.stamp
    |   |       gen_localizations.stamp
    |   |       outputs.json
    |   |       _composite.stamp
    |   |       
    |   +---flutter_assets
    |   |   |   AssetManifest.bin
    |   |   |   AssetManifest.bin.json
    |   |   |   FontManifest.json
    |   |   |   kernel_blob.bin
    |   |   |   NativeAssetsManifest.json
    |   |   |   NOTICES
    |   |   |   NOTICES.Z
    |   |   |   
    |   |   +---fonts
    |   |   |       MaterialIcons-Regular.otf
    |   |   |       
    |   |   +---packages
    |   |   |   \---cupertino_icons
    |   |   |       \---assets
    |   |   |               CupertinoIcons.ttf
    |   |   |               
    |   |   \---shaders
    |   |           ink_sparkle.frag
    |   |           stretch_effect.frag
    |   |           
    |   +---native_assets
    |   |   \---windows
    |   \---windows
    |       \---x64
    |           |   ALL_BUILD.vcxproj
    |           |   ALL_BUILD.vcxproj.filters
    |           |   CMakeCache.txt
    |           |   cmake_install.cmake
    |           |   INSTALL.vcxproj
    |           |   INSTALL.vcxproj.filters
    |           |   install_manifest.txt
    |           |   mobile.sln
    |           |   ZERO_CHECK.vcxproj
    |           |   ZERO_CHECK.vcxproj.filters
    |           |   
    |           +---CMakeFiles
    |           |   |   cmake.check_cache
    |           |   |   CMakeOutput.log
    |           |   |   generate.stamp
    |           |   |   generate.stamp.depend
    |           |   |   generate.stamp.list
    |           |   |   TargetDirectories.txt
    |           |   |   
    |           |   +---3.20.21032501-MSVC_2
    |           |   |   |   CMakeCXXCompiler.cmake
    |           |   |   |   CMakeDetermineCompilerABI_CXX.bin
    |           |   |   |   CMakeRCCompiler.cmake
    |           |   |   |   CMakeSystem.cmake
    |           |   |   |   VCTargetsPath.txt
    |           |   |   |   VCTargetsPath.vcxproj
    |           |   |   |   
    |           |   |   +---CompilerIdCXX
    |           |   |   |   |   CMakeCXXCompilerId.cpp
    |           |   |   |   |   CompilerIdCXX.exe
    |           |   |   |   |   CompilerIdCXX.vcxproj
    |           |   |   |   |   
    |           |   |   |   +---Debug
    |           |   |   |   |   |   CMakeCXXCompilerId.obj
    |           |   |   |   |   |   CompilerIdCXX.exe.recipe
    |           |   |   |   |   |   
    |           |   |   |   |   \---CompilerIdCXX.tlog
    |           |   |   |   |           CL.command.1.tlog
    |           |   |   |   |           CL.read.1.tlog
    |           |   |   |   |           CL.write.1.tlog
    |           |   |   |   |           CompilerIdCXX.lastbuildstate
    |           |   |   |   |           link.command.1.tlog
    |           |   |   |   |           link.read.1.tlog
    |           |   |   |   |           link.write.1.tlog
    |           |   |   |   |           
    |           |   |   |   \---tmp
    |           |   |   \---x64
    |           |   |       \---Debug
    |           |   |           |   VCTargetsPath.recipe
    |           |   |           |   
    |           |   |           \---VCTargetsPath.tlog
    |           |   |                   VCTargetsPath.lastbuildstate
    |           |   |                   
    |           |   +---49dbf824f8117ac3abcbf7acf369c30a
    |           |   |       INSTALL_force.rule
    |           |   |       
    |           |   +---a73f9d28acb0d4a3796b64d43db533ad
    |           |   |       generate.stamp.rule
    |           |   |       INSTALL_force.rule
    |           |   |       
    |           |   +---c324dd21c0ee8ea539b02680302e11ff
    |           |   |       flutter_windows.dll.rule
    |           |   |       
    |           |   +---cf44734bab8c57ece583991184560b4d
    |           |   |       flutter_assemble.rule
    |           |   |       INSTALL_force.rule
    |           |   |       
    |           |   \---CMakeTmp
    |           +---flutter
    |           |   |   cmake_install.cmake
    |           |   |   flutter_assemble.vcxproj
    |           |   |   flutter_assemble.vcxproj.filters
    |           |   |   flutter_wrapper_app.vcxproj
    |           |   |   flutter_wrapper_app.vcxproj.filters
    |           |   |   flutter_wrapper_plugin.vcxproj
    |           |   |   flutter_wrapper_plugin.vcxproj.filters
    |           |   |   INSTALL.vcxproj
    |           |   |   INSTALL.vcxproj.filters
    |           |   |   
    |           |   +---CMakeFiles
    |           |   |       generate.stamp
    |           |   |       generate.stamp.depend
    |           |   |       
    |           |   +---Debug
    |           |   |       flutter_wrapper_app.lib
    |           |   |       flutter_wrapper_app.pdb
    |           |   |       flutter_wrapper_plugin.lib
    |           |   |       flutter_wrapper_plugin.pdb
    |           |   |       
    |           |   +---flutter_wrapper_app.dir
    |           |   |   \---Debug
    |           |   |       |   core_implementations.obj
    |           |   |       |   flutter_engine.obj
    |           |   |       |   flutter_view_controller.obj
    |           |   |       |   flutter_wrapper_app.lib.recipe
    |           |   |       |   standard_codec.obj
    |           |   |       |   
    |           |   |       \---flutter_.772A594C.tlog
    |           |   |               CL.command.1.tlog
    |           |   |               CL.read.1.tlog
    |           |   |               CL.write.1.tlog
    |           |   |               CustomBuild.command.1.tlog
    |           |   |               CustomBuild.read.1.tlog
    |           |   |               CustomBuild.write.1.tlog
    |           |   |               flutter_wrapper_app.lastbuildstate
    |           |   |               Lib-link.read.1.tlog
    |           |   |               Lib-link.write.1.tlog
    |           |   |               Lib.command.1.tlog
    |           |   |               
    |           |   +---flutter_wrapper_plugin.dir
    |           |   |   \---Debug
    |           |   |       |   core_implementations.obj
    |           |   |       |   flutter_wrapper_plugin.lib.recipe
    |           |   |       |   plugin_registrar.obj
    |           |   |       |   standard_codec.obj
    |           |   |       |   
    |           |   |       \---flutter_.1B64E833.tlog
    |           |   |               CL.command.1.tlog
    |           |   |               CL.read.1.tlog
    |           |   |               CL.write.1.tlog
    |           |   |               CustomBuild.command.1.tlog
    |           |   |               CustomBuild.read.1.tlog
    |           |   |               CustomBuild.write.1.tlog
    |           |   |               flutter_wrapper_plugin.lastbuildstate
    |           |   |               Lib-link.read.1.tlog
    |           |   |               Lib-link.write.1.tlog
    |           |   |               Lib.command.1.tlog
    |           |   |               
    |           |   \---x64
    |           |       \---Debug
    |           |           \---flutter_assemble
    |           |               |   flutter_assemble.recipe
    |           |               |   
    |           |               \---flutter_assemble.tlog
    |           |                       CustomBuild.command.1.tlog
    |           |                       CustomBuild.read.1.tlog
    |           |                       CustomBuild.write.1.tlog
    |           |                       flutter_assemble.lastbuildstate
    |           |                       
    |           +---runner
    |           |   |   ALL_BUILD.vcxproj
    |           |   |   ALL_BUILD.vcxproj.filters
    |           |   |   cmake_install.cmake
    |           |   |   INSTALL.vcxproj
    |           |   |   INSTALL.vcxproj.filters
    |           |   |   mobile.vcxproj
    |           |   |   mobile.vcxproj.filters
    |           |   |   runner.sln
    |           |   |   
    |           |   +---CMakeFiles
    |           |   |       generate.stamp
    |           |   |       generate.stamp.depend
    |           |   |       
    |           |   +---Debug
    |           |   |   |   flutter_windows.dll
    |           |   |   |   mobile.exe
    |           |   |   |   mobile.pdb
    |           |   |   |   
    |           |   |   \---data
    |           |   |       |   icudtl.dat
    |           |   |       |   
    |           |   |       \---flutter_assets
    |           |   |           |   AssetManifest.bin
    |           |   |           |   AssetManifest.bin.json
    |           |   |           |   FontManifest.json
    |           |   |           |   kernel_blob.bin
    |           |   |           |   NativeAssetsManifest.json
    |           |   |           |   NOTICES
    |           |   |           |   NOTICES.Z
    |           |   |           |   
    |           |   |           +---fonts
    |           |   |           |       MaterialIcons-Regular.otf
    |           |   |           |       
    |           |   |           +---packages
    |           |   |           |   \---cupertino_icons
    |           |   |           |       \---assets
    |           |   |           |               CupertinoIcons.ttf
    |           |   |           |               
    |           |   |           \---shaders
    |           |   |                   ink_sparkle.frag
    |           |   |                   stretch_effect.frag
    |           |   |                   
    |           |   \---mobile.dir
    |           |       \---Debug
    |           |           |   flutter_window.obj
    |           |           |   generated_plugin_registrant.obj
    |           |           |   main.obj
    |           |           |   mobile.exe.recipe
    |           |           |   mobile.ilk
    |           |           |   Runner.res
    |           |           |   utils.obj
    |           |           |   vc142.pdb
    |           |           |   win32_window.obj
    |           |           |   
    |           |           \---mobile.tlog
    |           |                   CL.command.1.tlog
    |           |                   CL.read.1.tlog
    |           |                   CL.write.1.tlog
    |           |                   CustomBuild.command.1.tlog
    |           |                   CustomBuild.read.1.tlog
    |           |                   CustomBuild.write.1.tlog
    |           |                   link.command.1.tlog
    |           |                   link.read.1.tlog
    |           |                   link.write.1.tlog
    |           |                   mobile.lastbuildstate
    |           |                   rc.command.1.tlog
    |           |                   rc.read.1.tlog
    |           |                   rc.write.1.tlog
    |           |                   
    |           \---x64
    |               \---Debug
    |                   +---ALL_BUILD
    |                   |   |   ALL_BUILD.recipe
    |                   |   |   
    |                   |   \---ALL_BUILD.tlog
    |                   |           ALL_BUILD.lastbuildstate
    |                   |           CustomBuild.command.1.tlog
    |                   |           CustomBuild.read.1.tlog
    |                   |           CustomBuild.write.1.tlog
    |                   |           
    |                   +---INSTALL
    |                   |   |   INSTALL.recipe
    |                   |   |   
    |                   |   \---INSTALL.tlog
    |                   |           CustomBuild.command.1.tlog
    |                   |           CustomBuild.read.1.tlog
    |                   |           CustomBuild.write.1.tlog
    |                   |           INSTALL.lastbuildstate
    |                   |           
    |                   \---ZERO_CHECK
    |                       |   ZERO_CHECK.recipe
    |                       |   
    |                       \---ZERO_CHECK.tlog
    |                               CustomBuild.command.1.tlog
    |                               CustomBuild.read.1.tlog
    |                               CustomBuild.write.1.tlog
    |                               ZERO_CHECK.lastbuildstate
    |                               
    +---ios
    |   |   .gitignore
    |   |   
    |   +---Flutter
    |   |   |   AppFrameworkInfo.plist
    |   |   |   Debug.xcconfig
    |   |   |   flutter_export_environment.sh
    |   |   |   Generated.xcconfig
    |   |   |   Release.xcconfig
    |   |   |   
    |   |   \---ephemeral
    |   |           flutter_lldbinit
    |   |           flutter_lldb_helper.py
    |   |           
    |   +---Runner
    |   |   |   AppDelegate.swift
    |   |   |   GeneratedPluginRegistrant.h
    |   |   |   GeneratedPluginRegistrant.m
    |   |   |   Info.plist
    |   |   |   Runner-Bridging-Header.h
    |   |   |   
    |   |   +---Assets.xcassets
    |   |   |   +---AppIcon.appiconset
    |   |   |   |       Contents.json
    |   |   |   |       Icon-App-1024x1024@1x.png
    |   |   |   |       Icon-App-20x20@1x.png
    |   |   |   |       Icon-App-20x20@2x.png
    |   |   |   |       Icon-App-20x20@3x.png
    |   |   |   |       Icon-App-29x29@1x.png
    |   |   |   |       Icon-App-29x29@2x.png
    |   |   |   |       Icon-App-29x29@3x.png
    |   |   |   |       Icon-App-40x40@1x.png
    |   |   |   |       Icon-App-40x40@2x.png
    |   |   |   |       Icon-App-40x40@3x.png
    |   |   |   |       Icon-App-60x60@2x.png
    |   |   |   |       Icon-App-60x60@3x.png
    |   |   |   |       Icon-App-76x76@1x.png
    |   |   |   |       Icon-App-76x76@2x.png
    |   |   |   |       Icon-App-83.5x83.5@2x.png
    |   |   |   |       
    |   |   |   \---LaunchImage.imageset
    |   |   |           Contents.json
    |   |   |           LaunchImage.png
    |   |   |           LaunchImage@2x.png
    |   |   |           LaunchImage@3x.png
    |   |   |           README.md
    |   |   |           
    |   |   \---Base.lproj
    |   |           LaunchScreen.storyboard
    |   |           Main.storyboard
    |   |           
    |   +---Runner.xcodeproj
    |   |   |   project.pbxproj
    |   |   |   
    |   |   +---project.xcworkspace
    |   |   |   |   contents.xcworkspacedata
    |   |   |   |   
    |   |   |   \---xcshareddata
    |   |   |           IDEWorkspaceChecks.plist
    |   |   |           WorkspaceSettings.xcsettings
    |   |   |           
    |   |   \---xcshareddata
    |   |       \---xcschemes
    |   |               Runner.xcscheme
    |   |               
    |   +---Runner.xcworkspace
    |   |   |   contents.xcworkspacedata
    |   |   |   
    |   |   \---xcshareddata
    |   |           IDEWorkspaceChecks.plist
    |   |           WorkspaceSettings.xcsettings
    |   |           
    |   \---RunnerTests
    |           RunnerTests.swift
    |           
    +---lib
    |   |   main.dart
    |   |   
    |   +---config
    |   |       router.dart
    |   |       theme.dart
    |   |       
    |   +---core
    |   |   +---constants
    |   |   +---utils
    |   |   \---widgets
    |   +---features
    |   |   +---auth
    |   |   |   \---screens
    |   |   |           login_screen.dart
    |   |   |           
    |   |   +---dashboard
    |   |   |       dashboard_screen.dart
    |   |   |       
    |   |   +---devices
    |   |   |   \---screens
    |   |   |           device_alert_screen.dart
    |   |   |           device_list_screen.dart
    |   |   |           
    |   |   +---profile
    |   |   |   \---screens
    |   |   |           profile_screen.dart
    |   |   |           
    |   |   \---shops
    |   |       +---providers
    |   |       |       shop_provider.dart
    |   |       |       
    |   |       \---screens
    |   |               shop_list_screen.dart
    |   |               
    |   +---models
    |   +---providers
    |   \---services
    +---linux
    |   |   .gitignore
    |   |   CMakeLists.txt
    |   |   
    |   +---flutter
    |   |   |   CMakeLists.txt
    |   |   |   generated_plugins.cmake
    |   |   |   generated_plugin_registrant.cc
    |   |   |   generated_plugin_registrant.h
    |   |   |   
    |   |   \---ephemeral
    |   |       \---.plugin_symlinks
    |   |           +---flutter_blue_plus_linux
    |   |           |   |   analysis_options.yaml
    |   |           |   |   CHANGELOG.md
    |   |           |   |   LICENSE
    |   |           |   |   pubspec.yaml
    |   |           |   |   README.md
    |   |           |   |   
    |   |           |   \---lib
    |   |           |           flutter_blue_plus_linux.dart
    |   |           |           
    |   |           +---path_provider_linux
    |   |           |   |   AUTHORS
    |   |           |   |   CHANGELOG.md
    |   |           |   |   LICENSE
    |   |           |   |   pubspec.yaml
    |   |           |   |   README.md
    |   |           |   |   
    |   |           |   +---example
    |   |           |   |   |   pubspec.yaml
    |   |           |   |   |   README.md
    |   |           |   |   |   
    |   |           |   |   +---integration_test
    |   |           |   |   |       path_provider_test.dart
    |   |           |   |   |       
    |   |           |   |   +---lib
    |   |           |   |   |       main.dart
    |   |           |   |   |       
    |   |           |   |   +---linux
    |   |           |   |   |   |   CMakeLists.txt
    |   |           |   |   |   |   main.cc
    |   |           |   |   |   |   my_application.cc
    |   |           |   |   |   |   my_application.h
    |   |           |   |   |   |   
    |   |           |   |   |   \---flutter
    |   |           |   |   |           CMakeLists.txt
    |   |           |   |   |           generated_plugins.cmake
    |   |           |   |   |           
    |   |           |   |   \---test_driver
    |   |           |   |           integration_test.dart
    |   |           |   |           
    |   |           |   +---lib
    |   |           |   |   |   path_provider_linux.dart
    |   |           |   |   |   
    |   |           |   |   \---src
    |   |           |   |           get_application_id.dart
    |   |           |   |           get_application_id_real.dart
    |   |           |   |           get_application_id_stub.dart
    |   |           |   |           path_provider_linux.dart
    |   |           |   |           
    |   |           |   \---test
    |   |           |           get_application_id_test.dart
    |   |           |           path_provider_linux_test.dart
    |   |           |           
    |   |           \---shared_preferences_linux
    |   |               |   AUTHORS
    |   |               |   CHANGELOG.md
    |   |               |   LICENSE
    |   |               |   pubspec.yaml
    |   |               |   README.md
    |   |               |   
    |   |               +---example
    |   |               |   |   pubspec.yaml
    |   |               |   |   README.md
    |   |               |   |   
    |   |               |   +---integration_test
    |   |               |   |       shared_preferences_test.dart
    |   |               |   |       
    |   |               |   +---lib
    |   |               |   |       main.dart
    |   |               |   |       
    |   |               |   +---linux
    |   |               |   |   |   CMakeLists.txt
    |   |               |   |   |   main.cc
    |   |               |   |   |   my_application.cc
    |   |               |   |   |   my_application.h
    |   |               |   |   |   
    |   |               |   |   \---flutter
    |   |               |   |           CMakeLists.txt
    |   |               |   |           generated_plugins.cmake
    |   |               |   |           
    |   |               |   \---test_driver
    |   |               |           integration_test.dart
    |   |               |           
    |   |               +---lib
    |   |               |       shared_preferences_linux.dart
    |   |               |       
    |   |               \---test
    |   |                       fake_path_provider_linux.dart
    |   |                       legacy_shared_preferences_linux_test.dart
    |   |                       shared_preferences_linux_async_test.dart
    |   |                       
    |   \---runner
    |           CMakeLists.txt
    |           main.cc
    |           my_application.cc
    |           my_application.h
    |           
    +---macos
    |   |   .gitignore
    |   |   
    |   +---Flutter
    |   |   |   Flutter-Debug.xcconfig
    |   |   |   Flutter-Release.xcconfig
    |   |   |   GeneratedPluginRegistrant.swift
    |   |   |   
    |   |   \---ephemeral
    |   |           Flutter-Generated.xcconfig
    |   |           flutter_export_environment.sh
    |   |           
    |   +---Runner
    |   |   |   AppDelegate.swift
    |   |   |   DebugProfile.entitlements
    |   |   |   Info.plist
    |   |   |   MainFlutterWindow.swift
    |   |   |   Release.entitlements
    |   |   |   
    |   |   +---Assets.xcassets
    |   |   |   \---AppIcon.appiconset
    |   |   |           app_icon_1024.png
    |   |   |           app_icon_128.png
    |   |   |           app_icon_16.png
    |   |   |           app_icon_256.png
    |   |   |           app_icon_32.png
    |   |   |           app_icon_512.png
    |   |   |           app_icon_64.png
    |   |   |           Contents.json
    |   |   |           
    |   |   +---Base.lproj
    |   |   |       MainMenu.xib
    |   |   |       
    |   |   \---Configs
    |   |           AppInfo.xcconfig
    |   |           Debug.xcconfig
    |   |           Release.xcconfig
    |   |           Warnings.xcconfig
    |   |           
    |   +---Runner.xcodeproj
    |   |   |   project.pbxproj
    |   |   |   
    |   |   +---project.xcworkspace
    |   |   |   \---xcshareddata
    |   |   |           IDEWorkspaceChecks.plist
    |   |   |           
    |   |   \---xcshareddata
    |   |       \---xcschemes
    |   |               Runner.xcscheme
    |   |               
    |   +---Runner.xcworkspace
    |   |   |   contents.xcworkspacedata
    |   |   |   
    |   |   \---xcshareddata
    |   |           IDEWorkspaceChecks.plist
    |   |           
    |   \---RunnerTests
    |           RunnerTests.swift
    |           
    +---test
    |       widget_test.dart
    |       
    +---web
    |   |   favicon.png
    |   |   index.html
    |   |   manifest.json
    |   |   
    |   \---icons
    |           Icon-192.png
    |           Icon-512.png
    |           Icon-maskable-192.png
    |           Icon-maskable-512.png
    |           
    \---windows
        |   .gitignore
        |   CMakeLists.txt
        |   
        +---flutter
        |   |   CMakeLists.txt
        |   |   generated_plugins.cmake
        |   |   generated_plugin_registrant.cc
        |   |   generated_plugin_registrant.h
        |   |   
        |   \---ephemeral
        |       |   flutter_export.h
        |       |   flutter_messenger.h
        |       |   flutter_plugin_registrar.h
        |       |   flutter_texture_registrar.h
        |       |   flutter_windows.dll
        |       |   flutter_windows.dll.exp
        |       |   flutter_windows.dll.lib
        |       |   flutter_windows.dll.pdb
        |       |   flutter_windows.h
        |       |   generated_config.cmake
        |       |   icudtl.dat
        |       |   
        |       +---.plugin_symlinks
        |       |   +---path_provider_windows
        |       |   |   |   AUTHORS
        |       |   |   |   CHANGELOG.md
        |       |   |   |   LICENSE
        |       |   |   |   pubspec.yaml
        |       |   |   |   README.md
        |       |   |   |   
        |       |   |   +---example
        |       |   |   |   |   pubspec.yaml
        |       |   |   |   |   README.md
        |       |   |   |   |   
        |       |   |   |   +---integration_test
        |       |   |   |   |       path_provider_test.dart
        |       |   |   |   |       
        |       |   |   |   +---lib
        |       |   |   |   |       main.dart
        |       |   |   |   |       
        |       |   |   |   +---test_driver
        |       |   |   |   |       integration_test.dart
        |       |   |   |   |       
        |       |   |   |   \---windows
        |       |   |   |       |   CMakeLists.txt
        |       |   |   |       |   
        |       |   |   |       +---flutter
        |       |   |   |       |       CMakeLists.txt
        |       |   |   |       |       generated_plugins.cmake
        |       |   |   |       |       
        |       |   |   |       \---runner
        |       |   |   |           |   CMakeLists.txt
        |       |   |   |           |   flutter_window.cpp
        |       |   |   |           |   flutter_window.h
        |       |   |   |           |   main.cpp
        |       |   |   |           |   resource.h
        |       |   |   |           |   runner.exe.manifest
        |       |   |   |           |   Runner.rc
        |       |   |   |           |   run_loop.cpp
        |       |   |   |           |   run_loop.h
        |       |   |   |           |   utils.cpp
        |       |   |   |           |   utils.h
        |       |   |   |           |   win32_window.cpp
        |       |   |   |           |   win32_window.h
        |       |   |   |           |   
        |       |   |   |           \---resources
        |       |   |   |                   app_icon.ico
        |       |   |   |                   
        |       |   |   +---lib
        |       |   |   |   |   path_provider_windows.dart
        |       |   |   |   |   
        |       |   |   |   \---src
        |       |   |   |           folders.dart
        |       |   |   |           folders_stub.dart
        |       |   |   |           guid.dart
        |       |   |   |           path_provider_windows_real.dart
        |       |   |   |           path_provider_windows_stub.dart
        |       |   |   |           win32_wrappers.dart
        |       |   |   |           
        |       |   |   \---test
        |       |   |           guid_test.dart
        |       |   |           path_provider_windows_test.dart
        |       |   |           
        |       |   \---shared_preferences_windows
        |       |       |   AUTHORS
        |       |       |   CHANGELOG.md
        |       |       |   LICENSE
        |       |       |   pubspec.yaml
        |       |       |   README.md
        |       |       |   
        |       |       +---example
        |       |       |   |   AUTHORS
        |       |       |   |   LICENSE
        |       |       |   |   pubspec.yaml
        |       |       |   |   README.md
        |       |       |   |   
        |       |       |   +---integration_test
        |       |       |   |       shared_preferences_test.dart
        |       |       |   |       
        |       |       |   +---lib
        |       |       |   |       main.dart
        |       |       |   |       
        |       |       |   +---test_driver
        |       |       |   |       integration_test.dart
        |       |       |   |       
        |       |       |   \---windows
        |       |       |       |   CMakeLists.txt
        |       |       |       |   
        |       |       |       +---flutter
        |       |       |       |       CMakeLists.txt
        |       |       |       |       generated_plugins.cmake
        |       |       |       |       
        |       |       |       \---runner
        |       |       |           |   CMakeLists.txt
        |       |       |           |   flutter_window.cpp
        |       |       |           |   flutter_window.h
        |       |       |           |   main.cpp
        |       |       |           |   resource.h
        |       |       |           |   runner.exe.manifest
        |       |       |           |   Runner.rc
        |       |       |           |   run_loop.cpp
        |       |       |           |   run_loop.h
        |       |       |           |   utils.cpp
        |       |       |           |   utils.h
        |       |       |           |   win32_window.cpp
        |       |       |           |   win32_window.h
        |       |       |           |   
        |       |       |           \---resources
        |       |       |                   app_icon.ico
        |       |       |                   
        |       |       +---lib
        |       |       |       shared_preferences_windows.dart
        |       |       |       
        |       |       \---test
        |       |               fake_path_provider_windows.dart
        |       |               legacy_shared_preferences_windows_test.dart
        |       |               shared_preferences_windows_async_test.dart
        |       |               
        |       \---cpp_client_wrapper
        |           |   binary_messenger_impl.h
        |           |   byte_buffer_streams.h
        |           |   core_implementations.cc
        |           |   engine_method_result.cc
        |           |   flutter_engine.cc
        |           |   flutter_view_controller.cc
        |           |   plugin_registrar.cc
        |           |   readme
        |           |   standard_codec.cc
        |           |   texture_registrar_impl.h
        |           |   
        |           \---include
        |               \---flutter
        |                       basic_message_channel.h
        |                       binary_messenger.h
        |                       byte_streams.h
        |                       dart_project.h
        |                       encodable_value.h
        |                       engine_method_result.h
        |                       event_channel.h
        |                       event_sink.h
        |                       event_stream_handler.h
        |                       event_stream_handler_functions.h
        |                       flutter_engine.h
        |                       flutter_view.h
        |                       flutter_view_controller.h
        |                       message_codec.h
        |                       method_call.h
        |                       method_channel.h
        |                       method_codec.h
        |                       method_result.h
        |                       method_result_functions.h
        |                       plugin_registrar.h
        |                       plugin_registrar_windows.h
        |                       plugin_registry.h
        |                       standard_codec_serializer.h
        |                       standard_message_codec.h
        |                       standard_method_codec.h
        |                       texture_registrar.h
        |                       
        \---runner
            |   CMakeLists.txt
            |   flutter_window.cpp
            |   flutter_window.h
            |   main.cpp
            |   resource.h
            |   runner.exe.manifest
            |   Runner.rc
            |   utils.cpp
            |   utils.h
            |   win32_window.cpp
            |   win32_window.h
            |   
            \---resources
                    app_icon.ico
                    
