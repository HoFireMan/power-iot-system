// #C:\Code\PowerWork\power-iot-system\mobile\lib\config\theme.dart
import 'package:flutter/material.dart';
import 'package:google_fonts/google_fonts.dart';

class AppTheme {
  // --- 🎨 現代化配色系統 ---
  
  // 主色：更深沈穩重的森林綠，給人專業、信任感
  static const Color primaryColor = Color(0xFF2E7D32); 
  // 次要色：活潑的萊姆綠，用於強調數據或按鈕
  static const Color secondaryColor = Color(0xFF66BB6A);
  // [新增] 強調色：橘色 (用於星星、警示等)
  static const Color accentColor = Color(0xFFFF9800); 
  // 背景色：極淡的灰綠色，比純白更護眼，且能襯托白色卡片
  static const Color backgroundColor = Color(0xFFF1F8E9);
  // 錯誤色：柔和的紅
  static const Color errorColor = Color(0xFFE57373);
  // 卡片背景
  static const Color surfaceColor = Colors.white;

  // 文字顏色
  static const Color textPrimary = Color(0xFF1B5E20); // 深綠黑
  static const Color textSecondary = Color(0xFF546E7A); // 藍灰

  // 定義漸層 (用於 Profile Header)
  static const LinearGradient primaryGradient = LinearGradient(
    begin: Alignment.topLeft,
    end: Alignment.bottomRight,
    colors: [Color(0xFF2E7D32), Color(0xFF66BB6A)], 
  );

  static ThemeData get lightTheme {
    return ThemeData(
      useMaterial3: true,
      brightness: Brightness.light,
      
      // 色彩定義
      colorScheme: ColorScheme.fromSeed(
        seedColor: primaryColor,
        background: backgroundColor,
        surface: surfaceColor,
        primary: primaryColor,
        secondary: secondaryColor,
        error: errorColor,
        tertiary: accentColor, // 也可以設定 tertiary
      ),

      // 背景設定
      scaffoldBackgroundColor: backgroundColor,

      // 字體設定 (使用 Noto Sans TC)
      textTheme: GoogleFonts.notoSansTcTextTheme().apply(
        bodyColor: textPrimary,
        displayColor: textPrimary,
      ),

      // AppBar 樣式 (極簡化，去背)
      appBarTheme: const AppBarTheme(
        backgroundColor: Colors.transparent, // 透明背景
        elevation: 0,
        centerTitle: false, // 標題靠左，更現代
        titleTextStyle: TextStyle(
          color: textPrimary,
          fontSize: 24,
          fontWeight: FontWeight.bold,
          fontFamily: 'NotoSansTC',
        ),
        iconTheme: IconThemeData(color: textPrimary),
      ),

      // 卡片樣式 (懸浮感)
      cardTheme: CardThemeData(
        color: surfaceColor,
        elevation: 4, // 陰影高度
        shadowColor: primaryColor.withOpacity(0.15), // 帶有綠色的陰影
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(20), // 大圓角
        ),
        margin: const EdgeInsets.symmetric(vertical: 8, horizontal: 16),
      ),

      // 按鈕樣式 (膠囊狀)
      elevatedButtonTheme: ElevatedButtonThemeData(
        style: ElevatedButton.styleFrom(
          backgroundColor: primaryColor,
          foregroundColor: Colors.white,
          elevation: 2,
          padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 16),
          shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(30),
          ),
          textStyle: const TextStyle(
            fontSize: 16, 
            fontWeight: FontWeight.bold,
            letterSpacing: 1,
          ),
        ),
      ),
      
      // ICON 樣式
      iconTheme: const IconThemeData(
        color: primaryColor,
        size: 24,
      ),
    );
  }
}