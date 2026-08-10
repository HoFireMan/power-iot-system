import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../shops/providers/shop_provider.dart';
import '../../domain/repositories/admin_overview_repository.dart';
import '../providers/admin_overview_provider.dart';

class CreateMeasurementPointScreen extends ConsumerStatefulWidget {
  const CreateMeasurementPointScreen({super.key});

  @override
  ConsumerState<CreateMeasurementPointScreen> createState() =>
      _CreateMeasurementPointScreenState();
}

class _CreateMeasurementPointScreenState
    extends ConsumerState<CreateMeasurementPointScreen> {
  static const _failureMessage =
      'Unable to create Measurement Point. Please try again.';

  final _formKey = GlobalKey<FormState>();
  final _nameController = TextEditingController();
  bool _isSubmitting = false;
  String? _submissionError;

  @override
  void initState() {
    super.initState();
    final pending =
        ref.read(createMeasurementPointRequestIdentitySourceProvider).pending;
    if (pending != null && pending.name.isNotEmpty) {
      _nameController.text = pending.name;
    }
  }

  @override
  void dispose() {
    _nameController.dispose();
    super.dispose();
  }

  String? _validateName(String? value) {
    final name = value ?? '';
    if (name.trim().isEmpty) {
      return 'Measurement Point name is required.';
    }
    if (name.runes.length > 100) {
      return 'Measurement Point name must be 100 characters or fewer.';
    }
    return null;
  }

  Future<void> _submit() async {
    if (_isSubmitting || !_formKey.currentState!.validate()) {
      return;
    }

    setState(() {
      _isSubmitting = true;
      _submissionError = null;
    });

    final currentShop = ref.read(shopProvider).currentShop;
    final identitySource =
        ref.read(createMeasurementPointRequestIdentitySourceProvider);
    final pending = identitySource.pending;
    final requestShopId = pending?.shopId ?? currentShop.id;
    final requestName = pending?.name ?? _nameController.text;
    final requestIdentity = identitySource.identityFor(
      shopId: requestShopId,
      name: requestName,
    );
    late final String createdName;
    try {
      final point = await ref
          .read(adminOverviewRepositoryProvider)
          .createMeasurementPoint(
            CreateMeasurementPointInput(
              requestIdentity: requestIdentity,
              shopId: requestShopId,
              name: requestName,
            ),
          );
      createdName = point.name;
      identitySource.complete(requestIdentity);
    } catch (_) {
      if (!mounted) {
        return;
      }
      setState(() {
        _isSubmitting = false;
        _submissionError = _failureMessage;
      });
      return;
    }

    if (!mounted) {
      return;
    }
    setState(() {
      _isSubmitting = false;
    });
    context.pop(createdName);
  }

  @override
  Widget build(BuildContext context) {
    final currentShop = ref.watch(shopProvider).currentShop;
    final hasPendingUnresolvedRequest =
        ref.read(createMeasurementPointRequestIdentitySourceProvider).pending !=
            null;

    return PopScope(
      canPop: !_isSubmitting &&
          !hasPendingUnresolvedRequest &&
          _submissionError == null,
      child: Scaffold(
        appBar: AppBar(title: const Text('New Measurement Point')),
        body: SafeArea(
          child: Form(
            key: _formKey,
            child: ListView(
              padding: const EdgeInsets.all(20),
              children: [
                Text('Site: ${currentShop.name}'),
                const SizedBox(height: 20),
                TextFormField(
                  key: const Key('measurement-point-name-field'),
                  controller: _nameController,
                  enabled: !_isSubmitting &&
                      !hasPendingUnresolvedRequest &&
                      _submissionError == null,
                  decoration: const InputDecoration(
                    labelText: 'Measurement Point name',
                    border: OutlineInputBorder(),
                  ),
                  textInputAction: TextInputAction.done,
                  validator: _validateName,
                  onFieldSubmitted: (_) => _submit(),
                ),
                const SizedBox(height: 16),
                if (_submissionError case final error?) ...[
                  Text(
                    error,
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    style:
                        TextStyle(color: Theme.of(context).colorScheme.error),
                  ),
                  const SizedBox(height: 16),
                ],
                FilledButton(
                  onPressed: _isSubmitting ? null : _submit,
                  child: _isSubmitting
                      ? const SizedBox.square(
                          dimension: 20,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Text('Create Measurement Point'),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
