import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/core/network/remote_error.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/profile/data/repositories/profile_repository_impl.dart';
import 'package:power_iot_app/features/profile/domain/models/user_profile.dart';
import 'package:power_iot_app/features/profile/domain/repositories/profile_repository.dart';

enum RemoteStatus { loading, success, error, unauthorized }

class RemoteState<T> {
  const RemoteState.loading() : this._(RemoteStatus.loading);
  const RemoteState.success(this.data)
      : status = RemoteStatus.success,
        error = null;
  const RemoteState.error(this.error)
      : status = RemoteStatus.error,
        data = null;
  const RemoteState.unauthorized()
      : status = RemoteStatus.unauthorized,
        data = null,
        error = null;

  const RemoteState._(this.status)
      : data = null,
        error = null;

  final RemoteStatus status;
  final T? data;
  final Object? error;
}

final profileRepositoryProvider = Provider<ProfileRepository>((ref) {
  return RemoteProfileRepository(ref.watch(authClientProvider));
});

class ProfileNotifier extends StateNotifier<RemoteState<UserProfile>> {
  ProfileNotifier(this.repository, this.authClient)
      : super(const RemoteState<UserProfile>.loading()) {
    _epoch = authClient.authEpoch;
    authClient.addAuthEpochListener(_authEpochChanged);
    authClient.session.addListener(_sessionChanged);
    load();
  }

  final ProfileRepository repository;
  final AuthenticatedHttpClient authClient;
  int _request = 0;
  late int _epoch;
  bool _loadAfterAuthentication = false;

  void _authEpochChanged(int epoch) {
    if (!mounted || epoch == _epoch) return;
    _epoch = epoch;
    _request++;
    _loadAfterAuthentication = !authClient.isLogoutInProgress;
    state = const RemoteState<UserProfile>.loading();
  }

  void _sessionChanged() {
    if (!mounted || !authClient.session.isAuthenticated) {
      _loadAfterAuthentication = false;
      return;
    }
    if (_loadAfterAuthentication) {
      _loadAfterAuthentication = false;
      load();
    }
  }

  Future<void> load() async {
    final request = ++_request;
    final epoch = authClient.authEpoch;
    if (mounted) state = const RemoteState<UserProfile>.loading();
    try {
      final profile = await repository.fetchProfile();
      if (mounted &&
          request == _request &&
          authClient.isSessionCurrent(epoch)) {
        state = RemoteState<UserProfile>.success(profile);
      }
    } catch (error) {
      if (!mounted ||
          request != _request ||
          !authClient.isSessionCurrent(epoch)) {
        return;
      }
      if (isUnauthorizedError(error)) {
        await authClient.session.clearIfCurrent(
          () => authClient.isSessionCurrent(epoch),
        );
        if (mounted &&
            request == _request &&
            authClient.isSessionCurrent(epoch)) {
          state = const RemoteState<UserProfile>.unauthorized();
        }
      } else {
        state = RemoteState<UserProfile>.error(error);
      }
    }
  }

  @override
  void dispose() {
    authClient.removeAuthEpochListener(_authEpochChanged);
    authClient.session.removeListener(_sessionChanged);
    super.dispose();
  }
}

final profileProvider =
    StateNotifierProvider<ProfileNotifier, RemoteState<UserProfile>>((ref) {
  return ProfileNotifier(
    ref.watch(profileRepositoryProvider),
    ref.watch(authClientProvider),
  );
});
