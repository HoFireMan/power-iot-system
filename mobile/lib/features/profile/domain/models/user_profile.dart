/// Safe profile projection returned by GET /api/v1/me.
class UserProfile {
  const UserProfile({
    required this.id,
    required this.account,
    required this.name,
    required this.email,
    required this.phone,
    required this.isAdmin,
    required this.currentShopId,
  });

  final String id;
  final String account;
  final String name;
  final String? email;
  final String? phone;
  final bool isAdmin;
  final String? currentShopId;

  factory UserProfile.fromJson(Object? value) {
    final map = _object(value, 'profile');
    _requireKeys(map, const {
      'id',
      'account',
      'name',
      'email',
      'phone',
      'isAdmin',
      'currentShopId',
    });
    return UserProfile(
      id: _string(map['id'], 'id'),
      account: _string(map['account'], 'account'),
      name: _string(map['name'], 'name'),
      email: _nullableString(map['email'], 'email'),
      phone: _nullableString(map['phone'], 'phone'),
      isAdmin: _bool(map['isAdmin'], 'isAdmin'),
      currentShopId: _nullableString(map['currentShopId'], 'currentShopId'),
    );
  }
}

Map<String, Object?> _object(Object? value, String name) {
  if (value is! Map || value.keys.any((key) => key is! String)) {
    throw FormatException('Invalid $name response');
  }
  return value.map((key, value) => MapEntry(key as String, value));
}

void _requireKeys(Map<String, Object?> value, Set<String> required) {
  if (value.length != required.length || !value.keys.every(required.contains)) {
    throw const FormatException('Invalid response shape');
  }
}

String _string(Object? value, String name) {
  if (value is! String || value.isEmpty) {
    throw FormatException('Invalid $name');
  }
  return value;
}

String? _nullableString(Object? value, String name) {
  if (value == null) return null;
  if (value is! String) throw FormatException('Invalid $name');
  return value;
}

bool _bool(Object? value, String name) {
  if (value is! bool) throw FormatException('Invalid $name');
  return value;
}
