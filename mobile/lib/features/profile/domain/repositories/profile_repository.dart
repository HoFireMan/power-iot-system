import '../models/user_profile.dart';

abstract interface class ProfileRepository {
  Future<UserProfile> fetchProfile();
}
