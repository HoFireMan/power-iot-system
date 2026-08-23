import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/profile/domain/models/user_profile.dart';
import 'package:power_iot_app/features/profile/domain/repositories/profile_repository.dart';

class RemoteProfileRepository implements ProfileRepository {
  const RemoteProfileRepository(this.client);

  final AuthenticatedHttpClient client;

  @override
  Future<UserProfile> fetchProfile() async {
    final response = await client.dio.get<Object?>('/api/v1/me');
    return UserProfile.fromJson(response.data);
  }
}
